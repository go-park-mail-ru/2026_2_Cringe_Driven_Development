package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func runtimeBase() map[string]string {
	return map[string]string{"JWT_SECRET": "jwt", "BFF_API_KEY": "bff-test-key"}
}

func testKey() string { return base64.StdEncoding.EncodeToString(make([]byte, 32)) }

func openstackValues() map[string]string {
	v := runtimeBase()
	for k, val := range map[string]string{
		"RUNTIME_ENABLED": "true", "RUNTIME_PROVIDER": "openstack", "RUNTIME_SECRETS_KEY": testKey(),
		"RUNTIME_OS_AUTH_URL": "https://auth", "RUNTIME_OS_USERNAME": "u", "RUNTIME_OS_PASSWORD": "pw-VALUE",
		"RUNTIME_OS_USER_DOMAIN_NAME": "d", "RUNTIME_OS_PROJECT_ID": "p", "RUNTIME_OS_REGION": "ru-1",
		"RUNTIME_AVAILABILITY_ZONE": "ru-1a", "RUNTIME_NETWORK_ID": "n", "RUNTIME_SUBNET_ID": "s",
		"RUNTIME_SECURITY_GROUP_ID": "sg", "RUNTIME_IMAGE_ID": "i", "RUNTIME_FLAVOR_ID": "f", "RUNTIME_VOLUME_TYPE": "fast",
		"RUNTIME_SSH_KEY_NAME": "k", "RUNTIME_SSH_PRIVATE_KEY_FILE": "/k",
		"RUNTIME_TLS_CA_CERT_FILE": "/ca.crt", "RUNTIME_TLS_CA_KEY_FILE": "/ca.key",
		"RUNTIME_TLS_CLIENT_CERT_FILE": "/c.crt", "RUNTIME_TLS_CLIENT_KEY_FILE": "/c.key",
		"RUNTIME_BLOCKED_PUBLIC_IPS": "203.0.113.1, 203.0.113.2", "RUNTIME_ENV_NAME": "production",
	} {
		v[k] = val
	}
	return v
}

func TestRuntimeDisabledByDefault(t *testing.T) {
	v := runtimeBase()
	v["RUNTIME_MAX_ACTIVE"] = "garbage"
	c, err := LoadFrom(settings(v))
	if err != nil || c.Runtime.Enabled {
		t.Fatalf("enabled=%v err=%v", c.Runtime.Enabled, err)
	}
}

func TestRuntimeDefaults(t *testing.T) {
	v := runtimeBase()
	v["RUNTIME_ENABLED"], v["RUNTIME_PROVIDER"], v["RUNTIME_SECRETS_KEY"] = "true", "fake", testKey()
	c, err := LoadFrom(settings(v))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Runtime
	if r.RootDiskGiB != 20 || r.KernelDNS != "1.1.1.1" || r.EnvName != "dev-fake" || r.SSHUser != "root" ||
		r.StartupTimeout != 20*time.Minute || r.AnsibleTimeout != 15*time.Minute || r.DrainTimeout != 30*time.Second ||
		r.DeleteTimeout != 10*time.Minute || r.ReconcileInterval != 5*time.Minute ||
		r.MaxConcurrentOps != 2 || r.MaxActive != 3 || r.WorkDir != "/var/lib/cellestial/runtime" ||
		r.AgentVersion != "dev" || r.PythonImage != "cellestial-kernel-python:local" || r.RImage != "cellestial-kernel-r:local" ||
		len(r.SecretsKey) != 32 {
		t.Fatalf("unexpected defaults: %+v", r)
	}
}

func TestRuntimeOpenStackFull(t *testing.T) {
	c, err := LoadFrom(settings(openstackValues()))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Runtime
	if r.OSPassword != "pw-VALUE" || r.EnvName != "production" || r.SubnetID != "s" || r.TLSCAKeyFile != "/ca.key" ||
		len(r.BlockedPublicIPs) != 2 || r.BlockedPublicIPs[1] != "203.0.113.2" {
		t.Fatalf("unexpected: %+v", r)
	}
}

func TestRuntimeOpenStackRequiresCredentials(t *testing.T) {
	v := openstackValues()
	delete(v, "RUNTIME_OS_PASSWORD")
	_, err := LoadFrom(settings(v))
	if err == nil || !strings.Contains(err.Error(), "RUNTIME_OS_PASSWORD") {
		t.Fatalf("err=%v", err)
	}
	v = openstackValues()
	delete(v, "RUNTIME_TLS_CLIENT_KEY_FILE")
	if _, err = LoadFrom(settings(v)); err == nil || !strings.Contains(err.Error(), "RUNTIME_TLS_CLIENT_KEY_FILE") {
		t.Fatalf("err=%v", err)
	}
}

func TestRuntimeOpenStackRequiresEnvName(t *testing.T) {
	v := openstackValues()
	delete(v, "RUNTIME_ENV_NAME")
	if _, err := LoadFrom(settings(v)); err == nil || !strings.Contains(err.Error(), "RUNTIME_ENV_NAME") {
		t.Fatalf("err=%v", err)
	}
}

func TestRuntimeFakeEnvNameOverride(t *testing.T) {
	v := runtimeBase()
	v["RUNTIME_ENABLED"], v["RUNTIME_PROVIDER"], v["RUNTIME_SECRETS_KEY"] = "true", "fake", testKey()
	v["RUNTIME_ENV_NAME"] = "local"
	c, err := LoadFrom(settings(v))
	if err != nil || c.Runtime.EnvName != "local" {
		t.Fatalf("env=%q err=%v", c.Runtime.EnvName, err)
	}
}

func TestRuntimeRequiresSecretsKey32Bytes(t *testing.T) {
	for name, key := range map[string]string{"missing": "", "short": base64.StdEncoding.EncodeToString(make([]byte, 16)), "notbase64": "!!!secret-value"} {
		t.Run(name, func(t *testing.T) {
			v := runtimeBase()
			v["RUNTIME_ENABLED"], v["RUNTIME_PROVIDER"] = "true", "fake"
			if key != "" {
				v["RUNTIME_SECRETS_KEY"] = key
			}
			_, err := LoadFrom(settings(v))
			if err == nil || !strings.Contains(err.Error(), "RUNTIME_SECRETS_KEY") || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRuntimeRequiresBlockedPublicIPs(t *testing.T) {
	for _, val := range []string{"", " , ", "203.0.113.1,not-an-ip"} {
		v := openstackValues()
		v["RUNTIME_BLOCKED_PUBLIC_IPS"] = val
		_, err := LoadFrom(settings(v))
		if err == nil || !strings.Contains(err.Error(), "RUNTIME_BLOCKED_PUBLIC_IPS") || strings.Contains(err.Error(), "not-an-ip") {
			t.Fatalf("val=%q err=%v", val, err)
		}
	}
}

func TestRuntimeFakeNeedsNoCloud(t *testing.T) {
	v := runtimeBase()
	v["RUNTIME_ENABLED"], v["RUNTIME_PROVIDER"], v["RUNTIME_SECRETS_KEY"] = "true", "fake", testKey()
	if _, err := LoadFrom(settings(v)); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeUnknownProvider(t *testing.T) {
	v := runtimeBase()
	v["RUNTIME_ENABLED"], v["RUNTIME_PROVIDER"], v["RUNTIME_SECRETS_KEY"] = "true", "aws-secret", testKey()
	_, err := LoadFrom(settings(v))
	if err == nil || !strings.Contains(err.Error(), "RUNTIME_PROVIDER") || strings.Contains(err.Error(), "aws-secret") {
		t.Fatalf("err=%v", err)
	}
}

func TestRuntimeInvalidNumbersAndDurations(t *testing.T) {
	for k, val := range map[string]string{"RUNTIME_MAX_ACTIVE": "0", "RUNTIME_MAX_CONCURRENT_OPS": "x", "RUNTIME_STARTUP_TIMEOUT": "soon", "RUNTIME_ROOT_DISK_GIB": "-1"} {
		v := runtimeBase()
		v["RUNTIME_ENABLED"], v["RUNTIME_PROVIDER"], v["RUNTIME_SECRETS_KEY"] = "true", "fake", testKey()
		v[k] = val
		if _, err := LoadFrom(settings(v)); err == nil || !strings.Contains(err.Error(), k) {
			t.Fatalf("%s=%s err=%v", k, val, err)
		}
	}
}

func TestRuntimeInvalidEnabled(t *testing.T) {
	v := runtimeBase()
	v["RUNTIME_ENABLED"] = "maybe"
	if _, err := LoadFrom(settings(v)); err == nil || !strings.Contains(err.Error(), "RUNTIME_ENABLED") {
		t.Fatalf("err=%v", err)
	}
}
