package cronpad

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/gowthamgts/cronpad/internal/mcpserver"
)

type clientOptions struct {
	base, key string
	asJSON    bool
}

func (o *clientOptions) flags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&o.base, "url", os.Getenv("CRONPAD_URL"), "server base URL (CRONPAD_URL)")
	cmd.PersistentFlags().StringVar(&o.key, "api-key", os.Getenv("CRONPAD_API_KEY"), "API key (CRONPAD_API_KEY)")
	cmd.PersistentFlags().BoolVar(&o.asJSON, "json", false, "machine-readable JSON")
}
func (o *clientOptions) request(cmd *cobra.Command, method, path string, body []byte) ([]byte, error) {
	if o.key == "" {
		return nil, fmt.Errorf("set CRONPAD_API_KEY or --api-key")
	}
	endpoint, err := mcpserver.Endpoint(o.base, "/api/v1"+path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := mcpserver.HTTPClient(o.key)
	client.Timeout = 30 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("API response: %w", err)
	}
	if resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code, Message string
				Fields        map[string]string
			}
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Code != "" {
			fields, _ := json.Marshal(envelope.Error.Fields)
			if len(envelope.Error.Fields) > 0 {
				return nil, fmt.Errorf("%s: %s (%s)", envelope.Error.Code, envelope.Error.Message, fields)
			}
			return nil, fmt.Errorf("%s: %s", envelope.Error.Code, envelope.Error.Message)
		}
		return nil, fmt.Errorf("API returned HTTP %d", resp.StatusCode)
	}
	return data, nil
}
func (o *clientOptions) output(cmd *cobra.Command, data []byte, runs bool) error {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte(`{"ok":true}`)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("invalid API JSON: %w", err)
	}
	if o.asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
	}
	var rows []any
	object, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("unexpected API response")
	}
	if list, ok := object["data"].([]any); ok {
		rows = list
	} else if _, ok := object["id"]; ok {
		rows = []any{object}
	} else if id, ok := object["run_id"].(string); ok {
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintf(w, "RUN ID\n%s\n", id)
		return w.Flush()
	} else {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if runs {
		_, _ = fmt.Fprintln(w, "ID\tSTATUS\tTRIGGER\tSCHEDULED AT")
	} else {
		_, _ = fmt.Fprintln(w, "ID\tNAME\tSTATE\tSCHEDULE\tTIMEZONE")
	}
	field := func(v any) string {
		if v == nil {
			return "-"
		}
		return strings.Map(func(r rune) rune {
			if r < ' ' || r == 127 {
				return ' '
			}
			return r
		}, fmt.Sprint(v))
	}
	for _, row := range rows {
		r, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if runs {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", field(r["id"]), field(r["status"]), field(r["trigger"]), field(r["scheduled_at"]))
		} else {
			sc, _ := r["schedule"].(map[string]any)
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", field(r["id"]), field(r["name"]), field(r["state"]), field(sc["expr"]), field(sc["timezone"]))
		}
	}
	if cursor, ok := object["next_cursor"].(string); ok && cursor != "" {
		_, _ = fmt.Fprintf(w, "Next cursor: %s\n", field(cursor))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if next, ok := object["next_runs"].([]any); ok {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Next runs:")
		for _, value := range next {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), " "+field(value))
		}
	}
	return nil
}
func jobsCommand() *cobra.Command {
	o := &clientOptions{}
	jobs := &cobra.Command{Use: "jobs", Short: "Manage jobs through the API"}
	o.flags(jobs)
	var state, tag, query, cursor string
	var limit int
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		q := url.Values{}
		for k, v := range map[string]string{"state": state, "tag": tag, "q": query, "cursor": cursor} {
			if v != "" {
				q.Set(k, v)
			}
		}
		q.Set("limit", fmt.Sprint(limit))
		data, err := o.request(cmd, "GET", "/jobs?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		return o.output(cmd, data, false)
	}}
	list.Flags().StringVar(&state, "state", "", "filter state")
	list.Flags().StringVar(&tag, "tag", "", "filter tag")
	list.Flags().StringVar(&query, "query", "", "search names")
	list.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	list.Flags().IntVar(&limit, "limit", 50, "page size, 1–200")
	jobs.AddCommand(list)
	for _, action := range []string{"get", "pause", "resume", "enable", "trigger", "runs"} {
		var status string
		var count int
		cmd := &cobra.Command{Use: action + " <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if strings.ContainsAny(args[0], "/?#") {
				return fmt.Errorf("invalid job ID")
			}
			method, path := "GET", "/jobs/"+url.PathEscape(args[0])
			if action != "get" {
				path += "/" + action
				if action != "runs" {
					method = "POST"
				} else {
					q := url.Values{}
					if status != "" {
						q.Set("status", status)
					}
					q.Set("limit", fmt.Sprint(count))
					path += "?" + q.Encode()
				}
			}
			data, err := o.request(cmd, method, path, nil)
			if err != nil {
				return err
			}
			return o.output(cmd, data, action == "runs")
		}}
		if action == "runs" {
			cmd.Flags().StringVar(&status, "status", "", "filter run status")
			cmd.Flags().IntVar(&count, "limit", 50, "page size")
		}
		jobs.AddCommand(cmd)
	}
	var name, expr, tz, hook, executable, file string
	var args, tags []string
	var retries int
	create := &cobra.Command{Use: "create", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var body []byte
		if file != "" {
			for _, flag := range []string{"name", "schedule", "tz", "url", "command", "arg", "retries", "tag"} {
				if cmd.Flags().Changed(flag) {
					return fmt.Errorf("-f cannot be combined with --%s", flag)
				}
			}
			var err error
			if file == "-" {
				body, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), (1<<20)+1))
			} else {
				body, err = os.ReadFile(file)
			}
			if err != nil {
				return fmt.Errorf("read job: %w", err)
			}
			var job map[string]any
			if len(body) > 1<<20 || json.Unmarshal(body, &job) != nil || job == nil {
				return fmt.Errorf("job file must contain a JSON object of at most 1 MiB")
			}
		} else {
			if name == "" || expr == "" {
				return fmt.Errorf("--name and --schedule are required")
			}
			if (hook == "") == (executable == "") {
				return fmt.Errorf("choose exactly one of --url (webhook) or --command")
			}
			if executable == "" && len(args) > 0 {
				return fmt.Errorf("--arg requires --command")
			}
			config := map[string]any{"url": hook}
			kind := "webhook"
			if executable != "" {
				kind = "command"
				config = map[string]any{"command": executable, "args": args}
			}
			job := map[string]any{"name": name, "schedule": map[string]any{"expr": expr, "timezone": tz}, "sink": map[string]any{"kind": kind, "config": config}, "tags": tags}
			if cmd.Flags().Changed("retries") {
				job["retry"] = map[string]any{"max_attempts": retries}
			}
			body, _ = json.Marshal(job)
		}
		data, err := o.request(cmd, "POST", "/jobs", body)
		if err != nil {
			return err
		}
		return o.output(cmd, data, false)
	}}
	create.Flags().StringVar(&name, "name", "", "job name")
	create.Flags().StringVar(&expr, "schedule", "", "schedule expression")
	create.Flags().StringVar(&tz, "tz", "UTC", "IANA timezone")
	create.Flags().StringVar(&hook, "url", "", "webhook destination URL")
	create.Flags().StringVar(&executable, "command", "", "command executable")
	create.Flags().StringArrayVar(&args, "arg", nil, "repeat for each command argument")
	create.Flags().StringSliceVar(&tags, "tag", []string{}, "repeat or comma-separate tags")
	create.Flags().IntVar(&retries, "retries", 5, "maximum attempts, including first attempt")
	create.Flags().StringVarP(&file, "file", "f", "", "API JSON body file, or - for stdin")
	jobs.AddCommand(create)
	return jobs
}
func mcpCommand() *cobra.Command {
	o := &clientOptions{}
	var endpointPath string
	cmd := &cobra.Command{Use: "mcp", Short: "Bridge MCP HTTP to stdio", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if o.key == "" {
			return fmt.Errorf("set CRONPAD_API_KEY or --api-key")
		}
		endpoint, err := mcpserver.Endpoint(o.base, endpointPath)
		if err != nil {
			return err
		}
		return mcpserver.Proxy(cmd.Context(), endpoint, o.key, &mcp.StdioTransport{})
	}}
	o.flags(cmd)
	cmd.Flags().StringVar(&endpointPath, "path", "/mcp", "configured mcp.path")
	return cmd
}
