package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"time"
)

// OutputPrinter writes human-readable scenario results to a writer.
type OutputPrinter struct {
	w io.Writer
}

// NewOutputPrinter creates a new OutputPrinter writing to w.
func NewOutputPrinter(w io.Writer) *OutputPrinter {
	return &OutputPrinter{w: w}
}

// PrintScenario prints the full scenario result including per-step details.
func (p *OutputPrinter) PrintScenario(name string, elapsed time.Duration, err error, steps []stepResult) {
	if err != nil {
		fmt.Fprintf(p.w, "✗ %s  %.1fs  FAILED\n", name, elapsed.Seconds())
	} else {
		fmt.Fprintf(p.w, "✓ %s  %.1fs\n", name, elapsed.Seconds())
	}

	for _, s := range steps {
		if s.Err != nil {
			fmt.Fprintf(p.w, "  ✗ %-45s %.1fs  %s\n", s.Name, s.Duration.Seconds(), s.Err.Error())
		} else {
			fmt.Fprintf(p.w, "  ✓ %-45s %.1fs\n", s.Name, s.Duration.Seconds())
		}
	}
}

// JUnitTestSuites is the root XML element for JUnit output.
type JUnitTestSuites struct {
	XMLName  xml.Name        `xml:"testsuites"`
	TestSuite JUnitTestSuite `xml:"testsuite"`
}

// JUnitTestSuite is one test suite in JUnit output.
type JUnitTestSuite struct {
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Time      float64         `xml:"time,attr"`
	TestCases []JUnitTestCase `xml:"testcase"`
}

// JUnitTestCase is one test case in JUnit output.
type JUnitTestCase struct {
	Name    string        `xml:"name,attr"`
	Time    float64       `xml:"time,attr"`
	Failure *JUnitFailure `xml:"failure,omitempty"`
}

// JUnitFailure describes a test failure.
type JUnitFailure struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// WriteJUnit writes JUnit XML results to path.
func WriteJUnit(path, suiteName string, steps []stepResult, totalElapsed time.Duration) error {
	suite := JUnitTestSuite{
		Name:     suiteName,
		Tests:    len(steps),
		Time:     totalElapsed.Seconds(),
	}

	for _, s := range steps {
		tc := JUnitTestCase{
			Name: s.Name,
			Time: s.Duration.Seconds(),
		}

		if s.Err != nil {
			suite.Failures++
			tc.Failure = &JUnitFailure{
				Message: s.Err.Error(),
				Text:    s.Err.Error(),
			}
		}

		suite.TestCases = append(suite.TestCases, tc)
	}

	suites := JUnitTestSuites{TestSuite: suite}

	data, err := xml.MarshalIndent(suites, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JUnit XML: %w", err)
	}

	// Prepend XML header.
	xmlHeader := []byte(xml.Header)
	combined := append(xmlHeader, data...)
	combined = append(combined, '\n')

	if err := os.WriteFile(path, combined, 0o644); err != nil {
		return fmt.Errorf("write JUnit XML to %s: %w", path, err)
	}

	return nil
}
