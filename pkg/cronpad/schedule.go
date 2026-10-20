package cronpad

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/spf13/cobra"

	"github.com/gowthamgts/cronpad/internal/api"
	"github.com/gowthamgts/cronpad/internal/schedule"
)

func scheduleCommand(clk clockwork.Clock) *cobra.Command {
	root := &cobra.Command{Use: "schedule", Short: "Validate and preview schedules offline"}
	var kind, zone, after string
	var asJSON bool
	var n int
	root.PersistentFlags().StringVar(&kind, "kind", "auto", "cron, quartz, natural, rrule, once, or auto")
	root.PersistentFlags().StringVar(&zone, "tz", "UTC", "IANA timezone")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "JSON output")
	validate := &cobra.Command{Use: "validate <expr>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, _ := api.Validate(schedule.Spec{Kind: schedule.Kind(kind), Expr: args[0], Timezone: zone}, clk, 0)
		if asJSON {
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
				return fmt.Errorf("write validation: %w", err)
			}
		} else if result.Valid {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "valid\t%s\t%s\n", result.Kind, result.Description)
		}
		if !result.Valid {
			if len(result.Errors) > 0 {
				return result.Errors[0]
			}
			return fmt.Errorf("invalid schedule")
		}
		return nil
	}}
	next := &cobra.Command{Use: "next <expr>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if n < 1 || n > 100 {
			return fmt.Errorf("count must be between 1 and 100")
		}
		reference := clk
		at := clk.Now()
		if after != "" {
			parsed, err := time.Parse(time.RFC3339Nano, after)
			if err != nil {
				return fmt.Errorf("after: %w", err)
			}
			at = parsed
			reference = clockwork.NewFakeClockAt(at)
		}
		s, err := schedule.ParseWithClock(schedule.Spec{Kind: schedule.Kind(kind), Expr: args[0], Timezone: zone}, reference)
		if err != nil {
			return err
		}
		times := schedule.Upcoming(s, at, n)
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(api.Preview{Kind: s.Kind(), Canonical: s.Canonical(), Description: s.Describe(), Timezone: zone, Next: times})
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return fmt.Errorf("timezone: %w", err)
		}
		for _, t := range times {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s   (%s)\n", t.In(loc).Format("2006-01-02 15:04 MST"), t.Format(time.RFC3339)); err != nil {
				return fmt.Errorf("write preview: %w", err)
			}
		}
		return nil
	}}
	next.Flags().IntVarP(&n, "count", "n", 10, "number of occurrences (1–100)")
	next.Flags().StringVar(&after, "after", "", "reference time (RFC3339)")
	// Cobra exposes -n and --count; accept the documented short form without config loading.
	root.AddCommand(validate, next)
	return root
}
