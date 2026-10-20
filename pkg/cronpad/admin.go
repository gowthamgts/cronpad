package cronpad

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/gowthamgts/cronpad/internal/auth"
	"github.com/gowthamgts/cronpad/internal/clock"
	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/secrets"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"

	"strings"

	"github.com/spf13/cobra"
)

func adminCommands(path *string) []*cobra.Command {
	open := func(cmd *cobra.Command) (config.Config, *store.DBs, error) {
		c, e := config.Load(cmd.Context(), *path, nil, nil, nil)
		if e != nil {
			return c, nil, e
		}
		dbs, e := store.Open(cmd.Context(), c.Database.Path, c.Database.HistoryPath, c.Database.BusyTimeout.Duration(), c.Database.RunMigrations)
		return c, dbs, e
	}
	user := &cobra.Command{Use: "user", Short: "Manage local users"}
	var email, name string
	var asJSON bool
	key := &cobra.Command{Use: "create-key", Short: "Mint an API key for an existing user's personal workspace", RunE: func(cmd *cobra.Command, _ []string) error {
		if name == "" || len(name) > 200 {
			return fmt.Errorf("name must contain 1 to 200 bytes")
		}
		c, dbs, e := open(cmd)
		if e != nil {
			return e
		}
		defer func() { _ = dbs.Close() }()
		s := store.New(dbs)
		u, e := s.ControlReader().UserByEmail(cmd.Context(), strings.ToLower(email))
		if e != nil {
			return fmt.Errorf("user must have signed in first: %w", e)
		}
		ws, e := s.ControlReader().PersonalWorkspace(cmd.Context(), u.ID)
		if e != nil {
			return e
		}
		clk := clock.New()
		quota := &limits.Registry{Config: c.Limits, Store: s, Clock: clk}
		n, e := s.ControlReader().CountKeys(cmd.Context(), ws.ID)
		if e != nil {
			return e
		}
		if e = quota.Check(cmd.Context(), ws.ID, limits.Action{Limit: "max_api_keys_per_workspace", Value: n + 1}); e != nil {
			return e
		}
		service := auth.New(cmd.Context(), s, clk, c, nil)
		service.Limits = quota
		k, raw, e := service.MintKey(cmd.Context(), ws.ID, u.ID, name, []string{"*"})
		if e != nil {
			return e
		}
		aid, e := id.New(id.Run, clk.Now())
		if e != nil {
			return e
		}
		if e = s.History().WriteAudit(cmd.Context(), historygen.WriteAuditParams{ID: "audit_" + aid, WorkspaceID: ws.ID, Actor: "user:" + u.ID, Action: "api_key.create", Target: sql.NullString{String: k.ID, Valid: true}, CreatedAt: clk.Now().UnixMilli()}); e != nil {
			return e
		}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"id": k.ID, "key": raw})
		}
		_, e = fmt.Fprintln(cmd.OutOrStdout(), raw)
		return e
	}}
	key.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	key.Flags().StringVar(&email, "email", "", "existing user email")
	key.Flags().StringVar(&name, "name", "", "key name")
	_ = key.MarkFlagRequired("email")
	_ = key.MarkFlagRequired("name")
	user.AddCommand(key)
	secret := &cobra.Command{Use: "secrets", Short: "Manage encryption"}
	var newKey string
	rekey := &cobra.Command{Use: "rekey", Short: "Re-encrypt credentials while the server is stopped", RunE: func(cmd *cobra.Command, _ []string) error {
		c, dbs, e := open(cmd)
		if e != nil {
			return e
		}
		defer func() { _ = dbs.Close() }()
		v, e := secrets.New(c.Secrets.EncryptionKey, store.New(dbs))
		if e != nil {
			return e
		}
		if e = v.Migrate(cmd.Context()); e != nil {
			return e
		}
		if e = v.Rekey(cmd.Context(), newKey); e != nil {
			return e
		}
		_, e = fmt.Fprintln(cmd.OutOrStdout(), "Re-encrypted credentials. Restart with the new secrets.encryption_key.")
		return e
	}}
	rekey.Flags().StringVar(&newKey, "new-key", "", "new 32-byte base64 encryption key")
	_ = rekey.MarkFlagRequired("new-key")
	secret.AddCommand(rekey)
	migrate := &cobra.Command{Use: "migrate", Short: "Apply database migrations", RunE: func(cmd *cobra.Command, _ []string) error {
		_, dbs, e := open(cmd)
		if e != nil {
			return e
		}
		return dbs.Close()
	}}
	workspace := &cobra.Command{Use: "workspace", Short: "Manage local workspaces"}
	var reason string
	for _, suspend := range []bool{true, false} {
		verb := "unsuspend"
		if suspend {
			verb = "suspend"
		}
		command := &cobra.Command{Use: verb + " <slug>", Short: verb + " a workspace", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			_, dbs, err := open(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = dbs.Close() }()
			repository := store.New(dbs)
			ws, err := repository.ControlReader().WorkspaceBySlug(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("find workspace: %w", err)
			}
			if err = store.SetSuspension(cmd.Context(), repository, ws.ID, reason, suspend, clock.New().Now()); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), verb+"ed "+args[0])
			return err
		}}
		if suspend {
			command.Flags().StringVar(&reason, "reason", "", "operator reason")
			_ = command.MarkFlagRequired("reason")
		}
		workspace.AddCommand(command)
	}
	return []*cobra.Command{user, secret, migrate, workspace}
}
