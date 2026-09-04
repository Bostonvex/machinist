package environment

import (
	"os"
	"testing"
)

func TestDetectNativeEnvironmentWithoutLeakingValues(t *testing.T) {
	d := fakeDetector("darwin", "arm64", map[string]string{"SHELL": "/bin/zsh", "SECRET": "do-not-report"}, nil)
	manifest := detect(d, []string{" DGX-Spark ", "trusted", "trusted"})
	manifest.Digest = manifest.digest()
	if manifest.OS != "darwin" || manifest.Arch != "arm64" || manifest.Execution != "native" || manifest.Shell != "zsh" || manifest.PathStyle != "posix" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if len(manifest.Tags) != 2 || manifest.Tags[0] != "dgx-spark" || manifest.Tags[1] != "trusted" {
		t.Fatalf("tags = %#v", manifest.Tags)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate manifest: %v", err)
	}
}

func TestDetectWSLBeforeContainer(t *testing.T) {
	d := fakeDetector("linux", "amd64", map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, map[string]string{"/.dockerenv": ""})
	manifest := detect(d, nil)
	manifest.Digest = manifest.digest()
	if manifest.Execution != "wsl" {
		t.Fatalf("execution = %q, want wsl", manifest.Execution)
	}
}

func TestDetectWindowsCapabilities(t *testing.T) {
	d := fakeDetector("windows", "amd64", map[string]string{"COMSPEC": `C:\Windows\System32\cmd.exe`}, nil)
	manifest := detect(d, nil)
	manifest.Digest = manifest.digest()
	if manifest.Shell != "cmd" || manifest.PathStyle != "windows" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate manifest: %v", err)
	}
}

func TestValidateRejectsTamperingAndUnsafeValues(t *testing.T) {
	manifest := Detect([]string{"trusted"})
	manifest.Tags = append(manifest.Tags, "secret=value")
	if err := manifest.Validate(); err == nil {
		t.Fatal("unsafe tag was accepted")
	}
	// Tamper with a field the digest covers, using a value that is still a safe
	// identifier so the rejection can only come from the digest. The substitute
	// is chosen against the detected value: hardcoding one OS name makes this a
	// no-op on that platform, and the test then passes for no reason.
	manifest = Detect([]string{"trusted"})
	tampered := "linux"
	if manifest.OS == tampered {
		tampered = "darwin"
	}
	manifest.OS = tampered
	if err := manifest.Validate(); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
}

func TestEmptyManifestSupportsLegacyWorkers(t *testing.T) {
	if err := (Manifest{}).Validate(); err != nil {
		t.Fatalf("empty manifest: %v", err)
	}
}

func fakeDetector(goos, goarch string, environmentValues, files map[string]string) detector {
	return detector{
		goos: goos, goarch: goarch,
		lookupEnv: func(name string) (string, bool) {
			value, ok := environmentValues[name]
			return value, ok
		},
		readFile: func(name string) ([]byte, error) {
			value, ok := files[name]
			if !ok {
				return nil, os.ErrNotExist
			}
			return []byte(value), nil
		},
		stat: func(name string) (os.FileInfo, error) {
			if _, ok := files[name]; !ok {
				return nil, os.ErrNotExist
			}
			return nil, nil
		},
	}
}
