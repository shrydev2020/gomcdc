package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolveBuildIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{name: "missing build info", want: "devel"},
		{name: "release tag", ok: true, info: &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, want: "v1.2.3"},
		{name: "pseudo version", ok: true, info: &debug.BuildInfo{Main: debug.Module{Version: "v1.2.4-0.20260714010203-abcdef012345"}}, want: "v1.2.4-0.20260714010203-abcdef012345"},
		{name: "development revision", ok: true, info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef0123456789"}}}, want: "devel-abcdef012345"},
		{name: "dirty development revision", ok: true, info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef0123456789"}, {Key: "vcs.modified", Value: "true"}}}, want: "devel-abcdef012345-dirty"},
		{name: "dirty without revision", ok: true, info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}}, want: "devel-dirty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := resolve(test.info, test.ok); got != test.want {
				t.Fatalf("resolve() = %q, want %q", got, test.want)
			}
		})
	}
}
