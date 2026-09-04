package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestTraceBisect: bisect the pipeline by testing candidate stage outputs.
func TestTraceBisect(t *testing.T) {
	data, err := os.ReadFile(`F:/pitrading/_bt_cache/manifest.json`)
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Entries []struct {
			Slug string `json:"slug"`
			Code string `json:"code"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &man); err != nil {
		t.Fatal(err)
	}
	for _, e := range man.Entries {
		if e.Slug != "EMA-bands-leledc-bollinger-bands-trend-catching-strategy" {
			continue
		}
		code := strings.ReplaceAll(e.Code, "\r\n", "\n")
		lines := strings.Split(code, "\n")
		for i, ln := range lines {
			if strings.Contains(ln, "Visual Elements") {
				fmt.Printf("orig %d: %q\n", i+1, ln)
			}
		}
		for j := 49; j < 57 && j < len(lines); j++ {
			fmt.Printf("ctx %d: %q\n", j+1, lines[j])
		}
		// Corruption appears with the //**** comment line at line 56. Dump the
		// full preprocessed output for end=56 to see exactly what happened.
		sample := "\n" + strings.Join(lines[52:56], "\n") + "\n"
		fmt.Printf("SAMPLE: %q\n", sample)
		fmt.Printf("PREP:   %q\n", preprocess(sample))
		// Then dump each stage on SAMPLE by replicating the pipeline heads:
		s1 := stripBlockComments(sample)
		fmt.Printf("S1 block comments: %q\n", s1)
		s2 := stripLineComments(s1)
		fmt.Printf("S2 line comments:  %q\n", s2)
		s3 := joinTrailingOperatorLines(s2)
		fmt.Printf("S3 join trailing:  %q\n", s3)
		s4 := fixUnbalancedCloseLines(s3)
		fmt.Printf("S4 fix unbalanced: %q\n", s4)
		break
	}
}
