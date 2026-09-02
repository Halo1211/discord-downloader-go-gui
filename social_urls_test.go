package main

import (
	"strings"
	"testing"
)

func TestTwitterMediaURLWithQuery(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	input := "https://pbs.twimg.com/media/abc123?format=jpg&name=large"
	if !regexUrlTwitter.MatchString(input) {
		t.Fatalf("Twitter media URL did not match: %s", input)
	}
	links, err := getTwitterUrls(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := links["https://pbs.twimg.com/media/abc123?format=jpg&name=orig"]; !ok {
		t.Fatalf("Twitter media URL was not normalized: %#v", links)
	}
}

func TestSocialURLPatterns(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		regex interface{ MatchString(string) bool }
		input string
	}{
		{"Twitter status with query", regexUrlTwitterStatus, "https://x.com/user/status/123456789?s=20"},
		{"Instagram post with query", regexUrlInstagram, "https://www.instagram.com/p/ABC123/?utm_source=share"},
		{"Instagram reel", regexUrlInstagramReel, "https://instagram.com/reel/ABC123/"},
		{"Instagram TV", regexUrlInstagramTV, "https://instagram.com/tv/ABC123/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !test.regex.MatchString(test.input) {
				t.Fatalf("URL did not match: %s", test.input)
			}
		})
	}
}

func TestTistoryCDNURLExtractsEncodedOriginal(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	input := "https://blogthumb.pstatic.daumcdn.net/test/file.jpg/?scode=mtistory&fname=https%3A%2F%2Fabc.uf.tistory.com%2Fimage%2FABC123"
	links, err := getTistoryWithCDNUrls(input)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://abc.uf.tistory.com/original/ABC123"
	if _, exists := links[want]; !exists {
		t.Fatalf("getTistoryWithCDNUrls() = %#v, want %q", links, want)
	}
}

func TestTistoryOriginalQueryIsAppendedSafely(t *testing.T) {
	input := "https://t1.daumcdn.net/cfile/tistory/%E6%97%A5%E6%9C%AC%E8%AA%9E.png?token=abc#preview"
	links, err := getTistoryUrls(input)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://t1.daumcdn.net/cfile/tistory/%E6%97%A5%E6%9C%AC%E8%AA%9E.png?token=abc&original#preview"
	if _, exists := links[want]; !exists {
		t.Fatalf("getTistoryUrls() = %#v, want %q", links, want)
	}
	for link := range links {
		if strings.Count(link, "?") != 1 {
			t.Fatalf("Tistory URL contains an invalid second query delimiter: %q", link)
		}
	}
}

func TestSocialParsersRejectMalformedURLsWithoutPanicking(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	if _, err := getStreamableUrls("not a URL"); err == nil {
		t.Fatal("getStreamableUrls accepted a malformed URL")
	}

	oldAPIKey := config.Credentials.FlickrApiKey
	config.Credentials.FlickrApiKey = "test-key"
	t.Cleanup(func() { config.Credentials.FlickrApiKey = oldAPIKey })
	if _, err := getFlickrPhotoUrls("not a URL"); err == nil {
		t.Fatal("getFlickrPhotoUrls accepted a malformed URL")
	}
}
