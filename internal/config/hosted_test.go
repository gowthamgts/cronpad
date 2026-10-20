package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHostedFlipsDefaultsAndHonorsExplicitOverrides(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		hosted bool
	}{{"selfhosted", false}, {"hosted", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			if e := os.WriteFile(path, []byte("mode = \""+tc.mode+"\"\n"), 0600); e != nil {
				t.Fatal(e)
			}
			c, e := Load(context.Background(), path, nil, nil, nil)
			if e != nil {
				t.Fatal(e)
			}
			if c.Sinks.Command.Enabled == tc.hosted || c.Sinks.Webhook.AllowPrivateNetworks == tc.hosted {
				t.Fatal("unsafe hosted defaults", c.Sinks)
			}
			if tc.hosted {
				if !c.Auth.AllowSignup {
					t.Fatal("hosted signup closed")
				}
				if c.Limits.MaxJobsPerWorkspace != 100 || c.Limits.MinInterval.Duration() != time.Minute || c.Limits.MaxRunsPerMonthPerWorkspace != 50000 || c.Limits.MaxConcurrentRunsPerWorkspace != 5 || c.Limits.MaxAlertEmailsPerDay != 100 || c.Limits.MaxAPIKeysPerWorkspace != 20 || c.Limits.MaxRetryAttempts != 10 || c.Limits.APIRateLimitPerMinute != 600 || c.Sinks.Webhook.MaxTimeout.Duration() != 120*time.Second {
					t.Fatal(c.Limits, c.Sinks)
				}
			} else {
				if c.Auth.AllowSignup {
					t.Fatal("self-hosted signup open")
				}
				if c.Limits.MaxJobsPerWorkspace != 0 || c.Limits.MaxRunsPerDayPerWorkspace != 0 || c.Limits.MaxRetryAttempts != 0 || c.Limits.MaxAPIKeysPerWorkspace != 0 {
					t.Fatal("self-hosted quotas not unlimited", c.Limits)
				}
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if e := os.WriteFile(path, []byte("mode='hosted'\n[limits]\nmax_jobs_per_workspace=7\nmin_interval='2m'\n[sinks.command]\nenabled=true\n[sinks.webhook]\nallow_private_networks=true\n"), 0600); e != nil {
		t.Fatal(e)
	}
	c, e := Load(context.Background(), path, nil, nil, nil)
	if e != nil || c.Limits.MaxJobsPerWorkspace != 7 || c.Limits.MinInterval.Duration() != 2*time.Minute || !c.Sinks.Command.Enabled || !c.Sinks.Webhook.AllowPrivateNetworks {
		t.Fatal(c, e)
	}
}
