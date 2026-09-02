package main

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGetDomain(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "https", url: "https://cdn.discordapp.com/files/image.png?size=large", want: "cdn.discordapp.com"},
		{name: "subdomain", url: "https://media.example.test/path", want: "media.example.test"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := getDomain(test.url)
			if err != nil {
				t.Fatalf("getDomain() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("getDomain() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGetDomainRejectsInvalidURL(t *testing.T) {
	if got, err := getDomain("not a URL"); err == nil || got != "UNKNOWN" {
		t.Fatalf("getDomain() = %q, %v; want UNKNOWN and an error", got, err)
	}
}

func TestFilenameFromURL(t *testing.T) {
	tests := map[string]string{
		"https://example.test/path/archive.zip?download=1":                                                    "archive.zip",
		"https://example.test/path/my%20photo.png#preview":                                                    "my photo.png",
		"https://example.test/%E6%97%A5%E6%9C%AC%E8%AA%9E-%ED%95%9C%EA%B5%AD%EC%96%B4-%E4%B8%AD%E6%96%87.png": "日本語-한국어-中文.png",
	}
	for inputURL, want := range tests {
		if got := filenameFromURL(inputURL); got != want {
			t.Errorf("filenameFromURL(%q) = %q, want %q", inputURL, got, want)
		}
	}
}

func TestNormalizeDownloadURL(t *testing.T) {
	input := "https://cdn.discordapp.com/attachments/1/2/file.png?ex=123&is=456&hm=signature&format=webp"
	want := "https://cdn.discordapp.com/attachments/1/2/file.png?format=webp"
	if got := normalizeDownloadURL(input); got != want {
		t.Fatalf("normalizeDownloadURL() = %q, want %q", got, want)
	}
}

func TestNormalizeDownloadURLIgnoresFragments(t *testing.T) {
	withoutFragment := "https://example.test/files/image.png?size=large"
	withFragment := withoutFragment + "#preview"
	if got := normalizeDownloadURL(withFragment); got != withoutFragment {
		t.Fatalf("normalizeDownloadURL() = %q, want %q", got, withoutFragment)
	}
}

func TestNormalizeDownloadURLCanonicalizesHTTPAuthority(t *testing.T) {
	tests := map[string]string{
		"HTTPS://EXAMPLE.TEST:443/files/Image.PNG": "https://example.test/files/Image.PNG",
		"http://EXAMPLE.TEST:80/file.png":          "http://example.test/file.png",
		"http://EXAMPLE.TEST:8080/file.png":        "http://example.test:8080/file.png",
	}
	for inputURL, want := range tests {
		if got := normalizeDownloadURL(inputURL); got != want {
			t.Errorf("normalizeDownloadURL(%q) = %q, want %q", inputURL, got, want)
		}
	}
}

func TestLimitFilenamePreservesUnicodeAndExtension(t *testing.T) {
	input := strings.Repeat("界", 100) + ".png"
	got := limitFilenameBytes(input, 255)
	if !utf8.ValidString(got) {
		t.Fatalf("limitFilenameBytes() returned invalid UTF-8: %q", got)
	}
	if len(got) > 255 {
		t.Fatalf("limitFilenameBytes() returned %d bytes, want at most 255", len(got))
	}
	if !strings.HasSuffix(got, ".png") {
		t.Fatalf("limitFilenameBytes() lost the extension: %q", got)
	}
}

func TestFilenameWithSuffixPreservesUnicodeAndLimit(t *testing.T) {
	input := limitFilenameBytes(strings.Repeat("界", 100)+".png", 255)
	got := filenameWithSuffix(input, "-123456", 255)
	if !utf8.ValidString(got) || len(got) > 255 {
		t.Fatalf("filenameWithSuffix() returned invalid or oversized UTF-8 filename (%d bytes): %q", len(got), got)
	}
	if !strings.HasSuffix(got, "-123456.png") {
		t.Fatalf("filenameWithSuffix() lost suffix or extension: %q", got)
	}
}

func TestSafePathComponentRejectsTraversalAndWindowsDevices(t *testing.T) {
	for input, want := range map[string]string{
		"..":       "_",
		"... ":     "_",
		"CON":      "_CON",
		"lpt1.txt": "_lpt1.txt",
		"ok.png":   "ok.png",
		"a/b.png":  "ab.png",
	} {
		if got := safePathComponent(input); got != want {
			t.Errorf("safePathComponent(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPathIsWithinBase(t *testing.T) {
	base := t.TempDir()
	if !pathIsWithinBase(base, filepath.Join(base, "child", "file.txt")) {
		t.Fatal("valid child path was rejected")
	}
	if pathIsWithinBase(base, filepath.Join(base, "..", "escape.txt")) {
		t.Fatal("parent traversal path was accepted")
	}
}

func TestSafePathUnderBaseNeutralizesTraversalComponents(t *testing.T) {
	base := t.TempDir()
	got, err := safePathUnderBase(base, "..", `nested\\escape`, "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !pathIsWithinBase(base, got) {
		t.Fatalf("safePathUnderBase() escaped base: %q", got)
	}
	if filepath.Base(got) != "file.txt" {
		t.Fatalf("safePathUnderBase() filename = %q, want file.txt", filepath.Base(got))
	}
}
