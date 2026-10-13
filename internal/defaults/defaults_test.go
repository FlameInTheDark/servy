package defaults

import "testing"

func TestEnvOr(t *testing.T) {
	if got := envOr("SERVY_TEST_UNSET_KEY_XYZ", "fb"); got != "fb" {
		t.Fatalf("envOr unset = %q, want fallback %q", got, "fb")
	}
	t.Setenv("SERVY_TEST_SET_KEY_XYZ", "custom")
	if got := envOr("SERVY_TEST_SET_KEY_XYZ", "fb"); got != "custom" {
		t.Fatalf("envOr set = %q, want %q", got, "custom")
	}
	// empty value counts as unset — falls back instead of breaking docker refs
	t.Setenv("SERVY_TEST_EMPTY_KEY_XYZ", "")
	if got := envOr("SERVY_TEST_EMPTY_KEY_XYZ", "fb"); got != "fb" {
		t.Fatalf("envOr empty = %q, want fallback %q", got, "fb")
	}
}
