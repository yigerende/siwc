package service

import (
	"reflect"
	"testing"
)

func TestPluginRuntimeMemoryEnvironment(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "640MiB")
	t.Setenv("DATABASE_PASSWORD", "must-not-reach-plugin")
	t.Setenv("HTTPS_PROXY", "must-not-reach-plugin")
	if got := pluginRuntimeMemoryEnv(); !reflect.DeepEqual(got, []string{"GOMEMLIMIT=640MiB"}) {
		t.Fatalf("plugin received unexpected environment: %v", got)
	}
	t.Setenv("GOMEMLIMIT", "")
	if got := pluginRuntimeMemoryEnv(); len(got) != 0 {
		t.Fatalf("unset memory budget must preserve the isolated default: %v", got)
	}
}
