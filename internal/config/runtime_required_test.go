package config

import (
	"strings"
	"testing"
)

func TestRuntimeRequiredWithoutSwitch(t *testing.T) {
	for _, values := range []map[string]string{{}, {"RUNTIME_PROVIDER": "fake"}, {"RUNTIME_PROVIDER": "fake", "RUNTIME_SECRETS_KEY": "bad"}} {
		_, err := loadRuntime(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
		if err == nil {
			t.Fatal("incomplete runtime configuration accepted")
		}
	}
	cfg, err := loadRuntime(func(k string) (string, bool) {
		v, ok := map[string]string{"RUNTIME_PROVIDER": "fake", "RUNTIME_SECRETS_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}[k]
		return v, ok
	})
	if err != nil || cfg.Provider != "fake" {
		t.Fatalf("valid fake: %v", err)
	}
}

func TestRuntimeMissingProviderDoesNotLeakSecret(t *testing.T) {
	_, err := loadRuntime(func(k string) (string, bool) {
		if k == "RUNTIME_SECRETS_KEY" {
			return "PRIVATE-secret", true
		}
		return "", false
	})
	if err == nil || strings.Contains(err.Error(), "PRIVATE-secret") {
		t.Fatalf("unsafe config failure: %v", err)
	}
}

func TestRuntimeEachRequiredOpenStackSetting(t *testing.T) {
	for _, key := range []string{"RUNTIME_PROVIDER", "RUNTIME_SECRETS_KEY", "RUNTIME_ENV_NAME", "RUNTIME_OS_AUTH_URL", "RUNTIME_OS_USERNAME", "RUNTIME_OS_PASSWORD", "RUNTIME_OS_USER_DOMAIN_NAME", "RUNTIME_OS_PROJECT_ID", "RUNTIME_OS_REGION", "RUNTIME_AVAILABILITY_ZONE", "RUNTIME_NETWORK_ID", "RUNTIME_SUBNET_ID", "RUNTIME_SECURITY_GROUP_ID", "RUNTIME_IMAGE_ID", "RUNTIME_FLAVOR_ID", "RUNTIME_VOLUME_TYPE", "RUNTIME_SSH_KEY_NAME", "RUNTIME_SSH_PRIVATE_KEY_FILE", "RUNTIME_TLS_CA_CERT_FILE", "RUNTIME_TLS_CA_KEY_FILE", "RUNTIME_TLS_CLIENT_CERT_FILE", "RUNTIME_TLS_CLIENT_KEY_FILE", "RUNTIME_BLOCKED_PUBLIC_IPS"} {
		t.Run(key, func(t *testing.T) {
			values := openstackValues()
			delete(values, key)
			_, err := loadRuntime(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("missing %s: %v", key, err)
			}
		})
	}
}
