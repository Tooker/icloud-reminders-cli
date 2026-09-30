package utils

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"strings"
	"testing"
)

func TestTitleDocumentExactRoundTrip(t *testing.T) {
	titles := []string{"", "X", "❤️", "ÄÖÜ äöü ß", "Rollläden im Wohnzimmer getrennt steuerbar",
		"Nur noch schöne Dinge machen ❤️", "Glücklich sein und sich lieben! 🥰",
		"3,43 % Sparkasse Dortmund, 10 Jahre Zinsbindung", "6 Monate bereitstellungsfreie Zeit",
		"Arbeitsverträge von Wera an Herrn Moser senden", "Sondertilgung im November immer machen",
		"Vertragsvorlage ohne unsere Namen von einem anderen Objekt",
		"Alina fragen, ob sie eine Risikolebensversicherung abgeschlossen hat",
		"Unterlagen für die Bank zusammenstellen", "Bank anrufen und erfragen, welche Unterlagen benötigt werden",
		"!#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", "  preserve whitespace\nsecond line\t ", strings.Repeat("😀", 300)}
	// Cover every one-byte printable length, and multi-byte varint lengths.
	for n := 1; n <= 4096; n++ {
		titles = append(titles, strings.Repeat("x", n))
	}
	for _, title := range titles {
		encoded, err := EncodeTitle(title)
		if err != nil {
			t.Fatal(err)
		}
		if got := ExtractTitle(encoded); got != title {
			t.Fatalf("text differs at length %d", len(title))
		}
	}
}

func compressed(raw []byte) string {
	var out bytes.Buffer
	zip := gzip.NewWriter(&out)
	_, _ = zip.Write(raw)
	_ = zip.Close()
	return base64.StdEncoding.EncodeToString(out.Bytes())
}

func TestDocumentDecoderRejectsMalformedFramingAndMetadata(t *testing.T) {
	for _, raw := range [][]byte{
		{0x12, 0xff}, {0x12, 0x7f, 'x'}, {0x00}, {0x0a, 0x03, 'f', 'o', 'o'},
		append(EncodeField(2, 2, []byte{0x1a, 0x02, 0x12, 0x00}), EncodeField(2, 2, []byte{})...),
		[]byte(strings.Repeat("x", (1<<20)+1)),
	} {
		if got := ExtractTitle(compressed(raw)); got != "" {
			t.Fatal("malformed/metadata text escaped")
		}
	}
	if got := ExtractTitle("not-base64"); got != "" {
		t.Fatal("invalid base64 escaped")
	}
	text := "  plain legacy notes\nwith whitespace  "
	if got := ExtractTitle(base64.StdEncoding.EncodeToString([]byte(text))); got != text {
		t.Fatal("legacy text changed")
	}
}
