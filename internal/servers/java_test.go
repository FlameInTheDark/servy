package servers

import (
	"reflect"
	"testing"
)

func TestSuggestJavaForMinecraft(t *testing.T) {
	cases := map[string]int{
		"1.7.10":  8,
		"1.12.2":  8,
		"1.16.5":  8,
		"1.16":    8,
		"1.17":    16,
		"1.17.1":  16,
		"1.18":    17,
		"1.18.2":  17,
		"1.19.4":  17,
		"1.20.1":  17,
		"1.20.4":  17,
		"1.20.5":  21,
		"1.20.6":  21,
		"1.21":    21,
		"1.21.4":  21,
		"2.0":     21,
		"garbage": 8, // conservative default
	}
	for v, want := range cases {
		if got := SuggestJavaForMinecraft(v); got != want {
			t.Errorf("SuggestJavaForMinecraft(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestJavaMajor(t *testing.T) {
	cases := map[string]int{
		"1.8.0_412": 8,
		"1.8.0_392": 8,
		"11.0.22":   11,
		"17.0.10":   17,
		"21.0.3":    21,
		"25":        25,
		"unknown":   -1,
		"":          -1,
	}
	for v, want := range cases {
		if got := javaMajor(v); got != want {
			t.Errorf("javaMajor(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestApplyJavaSelection(t *testing.T) {
	// none / empty → unchanged
	argv := []string{"java", "-Xms1G", "-jar", "server.jar"}
	got := ApplyJavaSelection(argv, "")
	if !reflect.DeepEqual(got, argv) {
		t.Fatalf("empty selection changed argv: %v", got)
	}
	got = ApplyJavaSelection(argv, "none")
	if !reflect.DeepEqual(got, argv) {
		t.Fatalf("none selection changed argv: %v", got)
	}
	// "java" → literal java
	got = ApplyJavaSelection([]string{"/usr/lib/jvm/java-8/bin/java", "-jar", "s.jar"}, "java")
	if got[0] != "java" {
		t.Fatalf("java selection: argv[0] = %q, want java", got[0])
	}
	// executable path → exact binary
	got = ApplyJavaSelection(argv, "/usr/lib/jvm/java-21-openjdk-amd64/bin/java")
	if got[0] != "/usr/lib/jvm/java-21-openjdk-amd64/bin/java" {
		t.Fatalf("exe selection: argv[0] = %q", got[0])
	}
	// JVM home dir → <home>/bin/java
	got = ApplyJavaSelection(argv, "/usr/lib/jvm/java-21-openjdk-amd64")
	if got[0] != "/usr/lib/jvm/java-21-openjdk-amd64/bin/java" {
		t.Fatalf("home selection: argv[0] = %q", got[0])
	}
	// args preserved
	if len(got) != 4 || got[1] != "-Xms1G" || got[3] != "server.jar" {
		t.Fatalf("args not preserved: %v", got)
	}
}

func TestValidateJavaSelection(t *testing.T) {
	installs := []JavaInstall{
		{Path: "/usr/lib/jvm/java-21/bin/java", Home: "/usr/lib/jvm/java-21", Major: 21},
		{Path: "/usr/lib/jvm/java-8/bin/java", Home: "/usr/lib/jvm/java-8", Major: 8},
	}
	for _, ok := range []string{"", "none", "java", "/usr/lib/jvm/java-21/bin/java", "/usr/lib/jvm/java-8"} {
		if !ValidateJavaSelection(ok, installs) {
			t.Errorf("ValidateJavaSelection(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"/opt/nope", "/usr/lib/jvm/java-99/bin/java", "../escape"} {
		if ValidateJavaSelection(bad, installs) {
			t.Errorf("ValidateJavaSelection(%q) = true, want false", bad)
		}
	}
}

func TestDetectJavasFindsHostJava(t *testing.T) {
	// This host has JVMs or not — either way the call must not panic and must
	// be internally consistent (versions probed, majors parsed).
	SetExtraJavaPaths(nil)
	installs := DetectJavas()
	for _, j := range installs {
		if j.Path == "" {
			t.Errorf("install with empty path: %+v", j)
		}
		if j.Version == "" {
			t.Errorf("install with empty version: %+v", j)
		}
	}
	// cached path returns the same slice
	again := DetectJavas()
	if len(again) != len(installs) {
		t.Errorf("cache mismatch: %d vs %d", len(installs), len(again))
	}
}
