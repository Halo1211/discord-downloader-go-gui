package main

import (
	"fmt"
	"log"
	"math"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	pathBlacklist = []string{"/", "\\", "<", ">", ":", "\"", "|", "?", "*"}
)

func stringInSlice(a string, list []string) bool {
	for _, b := range list {
		if strings.EqualFold(a, b) {
			return true
		}
	}
	return false
}

func formatNumber(n int64) string {
	var numberSeparator byte = ','
	if config.EuropeanNumbers {
		numberSeparator = '.'
	}

	in := strconv.FormatInt(n, 10)
	out := make([]byte, len(in)+(len(in)-2+int(in[0]/'0'))/3)
	if in[0] == '-' {
		in, out[0] = in[1:], '-'
	}

	for i, j, k := len(in)-1, len(out)-1, 0; ; i, j = i-1, j-1 {
		out[j] = in[i]
		if i == 0 {
			return string(out)
		}
		if k++; k == 3 {
			j, k = j-1, 0
			out[j] = numberSeparator
		}
	}
}

func formatNumberShort(x int64) string {
	decimalSeparator := "."
	if config.EuropeanNumbers {
		decimalSeparator = ","
	}

	value := float64(x)
	if math.Abs(value) <= 1000 {
		return fmt.Sprint(x)
	}
	suffixes := []string{"k", "m", "b", "t", "q", "qi"}
	suffixIndex := -1
	for math.Abs(value) >= 1000 && suffixIndex < len(suffixes)-1 {
		value /= 1000
		suffixIndex++
	}
	// Preserve the old compact style: at most one decimal, truncated rather
	// than rounded so the displayed tally never exceeds the actual value.
	value = math.Trunc(value*10) / 10
	formatted := strings.TrimSuffix(fmt.Sprintf("%.1f", value), ".0")
	if decimalSeparator != "." {
		formatted = strings.Replace(formatted, ".", decimalSeparator, 1)
	}
	return formatted + suffixes[suffixIndex]
}

func pluralS(num int) string {
	if num == 1 {
		return ""
	}
	return "s"
}

func wrapHyphens(i string, l int) string {
	n := i
	if len(n) < l {
		n = "- " + n + " -"
		for len(n) < l {
			n = "-" + n + "-"
		}
	}
	return n
}

func wrapHyphensW(i string) string {
	return wrapHyphens(i, 90)
}

func stripSymbols(i string) string {
	re, err := regexp.Compile(`[^\w]`)
	if err != nil {
		log.Fatal(err)
	}
	return re.ReplaceAllString(i, " ")
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, character := range s {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func clearPathIllegalChars(p string) string {
	r := p
	for _, key := range pathBlacklist {
		r = strings.ReplaceAll(r, key, "")
	}
	return strings.Map(func(character rune) rune {
		if character < 32 || character == 127 {
			return -1
		}
		return character
	}, r)
}

func safePathComponent(value string) string {
	value = strings.TrimRight(strings.TrimSpace(clearPathIllegalChars(value)), ". ")
	if value == "" || value == "." || value == ".." {
		return "_"
	}
	base := value
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	upperBase := strings.ToUpper(base)
	reserved := upperBase == "CON" || upperBase == "PRN" || upperBase == "AUX" || upperBase == "NUL"
	if len(upperBase) == 4 && (strings.HasPrefix(upperBase, "COM") || strings.HasPrefix(upperBase, "LPT")) && upperBase[3] >= '1' && upperBase[3] <= '9' {
		reserved = true
	}
	if reserved {
		value = "_" + value
	}
	return value
}

func safePathUnderBase(basePath string, components ...string) (string, error) {
	target := filepath.Clean(basePath)
	for _, component := range components {
		target = filepath.Join(target, safePathComponent(component))
	}
	if !pathIsWithinBase(basePath, target) {
		return "", fmt.Errorf("path escapes base directory %q", basePath)
	}
	return target, nil
}

func clearDiacritics(p string) string {
	return norm.NFKC.String(p)
}

func clearNonAscii(p string) string {
	re := regexp.MustCompile("[[:^ascii:]]")
	return re.ReplaceAllLiteralString(p, "")
}

func clearDoubleSpaces(p string) string {
	ret := p
	for {
		ret = strings.ReplaceAll(ret, "  ", " ")
		if !strings.Contains(ret, "  ") {
			break
		}
	}
	if ret == "" {
		return p
	}
	return ret
}

func clearPaddedSymbols(p string) string { // currently just spaces
	return strings.Trim(p, " ")
}

func clearSourceField(p string, cfg configurationSource) string {
	ret := clearPathIllegalChars(p)

	// FilepathNormalizeText
	if cfg.FilepathNormalizeText != nil {
		if *cfg.FilepathNormalizeText {
			ret = clearDiacritics(ret)
		}
	}

	// FilepathStripSymbols
	if cfg.FilepathStripSymbols != nil {
		if *cfg.FilepathStripSymbols {
			ret = clearNonAscii(ret)
		}
	}

	// Clear illegal chars and clear extra space.
	ret = clearDoubleSpaces(clearPaddedSymbols(clearPathIllegalChars(ret)))

	// if there's nothing left, just return what originally came through, without illegal characters.
	if len(regexp.MustCompile(`[^a-zA-Z0-9 ]+`).ReplaceAllLiteralString(ret, "")) == 0 {
		return clearPathIllegalChars(p)
	}

	return ret
}

func clearSourceLogField(p string, cfg configurationSourceLog) string {
	ret := clearPathIllegalChars(p)

	// FilepathNormalizeText
	if cfg.FilepathNormalizeText != nil {
		if *cfg.FilepathNormalizeText {
			ret = clearDiacritics(ret)
		}
	}

	// FilepathStripSymbols
	if cfg.FilepathStripSymbols != nil {
		if *cfg.FilepathStripSymbols {
			ret = clearNonAscii(ret)
		}
	}

	// Clear illegal chars and clear extra space.
	ret = clearDoubleSpaces(clearPaddedSymbols(clearPathIllegalChars(ret)))

	// if there's nothing left, just return what originally came through, without illegal characters.
	if len(regexp.MustCompile(`[^a-zA-Z0-9 ]+`).ReplaceAllLiteralString(ret, "")) == 0 {
		return clearPathIllegalChars(p)
	}

	return ret
}

func filenameFromURL(inputURL string) string {
	// Parse the URL path instead of treating the entire URL as a filesystem
	// path. Queries and fragments are not part of the server-side filename,
	// and URL paths may contain escaped characters such as spaces.
	if parsedURL, err := url.Parse(inputURL); err == nil && parsedURL.Path != "" {
		if base := path.Base(parsedURL.Path); base != "." && base != "/" {
			return base
		}
	}
	// Preserve the historical best-effort result for malformed or path-less
	// input while still excluding URL metadata from the filename.
	withoutFragment := strings.SplitN(inputURL, "#", 2)[0]
	withoutQuery := strings.SplitN(withoutFragment, "?", 2)[0]
	return path.Clean(path.Base(withoutQuery))
}

// limitFilenameBytes keeps a filename within common filesystem component
// limits without splitting a multi-byte UTF-8 character. The extension is
// retained whenever it fits so type detection and duplicate naming still work.
func limitFilenameBytes(filename string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	if len(filename) <= maximum {
		return filename
	}
	truncate := func(value string, limit int) string {
		if limit <= 0 {
			return ""
		}
		if len(value) <= limit {
			return value
		}
		for limit > 0 && !utf8.RuneStart(value[limit]) {
			limit--
		}
		return value[:limit]
	}

	extension := filepath.Ext(filename)
	if extension == "" || len(extension) >= maximum {
		return truncate(filename, maximum)
	}
	stem := strings.TrimSuffix(filename, extension)
	return truncate(stem, maximum-len(extension)) + extension
}

func filenameWithSuffix(filename string, suffix string, maximum int) string {
	extension := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, extension)
	remaining := maximum - len(extension)
	if remaining <= 0 {
		return limitFilenameBytes(extension, maximum)
	}
	if len(suffix) > remaining {
		suffix = limitFilenameBytes(suffix, remaining)
		return suffix + extension
	}
	return limitFilenameBytes(stem, remaining-len(suffix)) + suffix + extension
}

func getDomain(inputURL string) (string, error) {
	parsedURL, err := url.ParseRequestURI(inputURL)
	if err != nil {
		return "UNKNOWN", err
	}
	if parsedURL.Hostname() == "" {
		return "UNKNOWN", fmt.Errorf("URL has no hostname: %q", inputURL)
	}
	return parsedURL.Hostname(), nil
}
