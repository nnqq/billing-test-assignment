package money

import (
	"errors"
	"testing"
)

func TestParseMinor(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"315.00", 31500},
		{"0", 0},
		{"0.00", 0},
		{"88.25", 8825},
		{"1608.17", 160817},
		{"4979.74", 497974},
		{"-2597.77", -259777},
		{"-1664.81", -166481},
		{"5.3550", 536},
		{"2.9040", 290},
		{"24.1226", 2412},
		{"51.9455", 5195},
		{"0.0000", 0},
		{"104.0129", 10401},
		{"1.0399", 104},
		{"0.005", 1},
		{"-0.005", -1},
		{"0.004", 0},
		{"0.5", 50},
		{".75", 75},
		{"+12.34", 1234},
		{"  7.10  ", 710},
		{"92233720368547758.07", 9223372036854775807},
	}

	for _, c := range cases {
		got, err := ParseMinor(c.in, "SAR")
		if err != nil {
			t.Errorf("ParseMinor(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMinor(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseMinorRejects(t *testing.T) {
	cases := []struct {
		in      string
		wantErr error
	}{
		{"", ErrEmpty},
		{"   ", ErrEmpty},
		{"abc", ErrMalformed},
		{"12.", ErrMalformed},
		{"1.2.3", ErrMalformed},
		{"1 234.00", ErrMalformed},
		{"1,234.00", ErrMalformed},
		{"-", ErrMalformed},
		{"12.3a", ErrMalformed},
		{"NaN", ErrMalformed},
		{"92233720368547758.08", ErrOutOfRange},
	}

	for _, c := range cases {
		_, err := ParseMinor(c.in, "SAR")
		if !errors.Is(err, c.wantErr) {
			t.Errorf("ParseMinor(%q) error = %v, want %v", c.in, err, c.wantErr)
		}
	}
}

// The CSV carries fees with four fraction digits, so eight of them land exactly
// on half a minor unit. Pin the rounding mode down.
func TestParseMinorRoundsHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"5.3550", 536},
		{"-5.3550", -536},
		{"5.3450", 535},
		{"5.3551", 536},
		{"5.3549", 535},
	}

	for _, c := range cases {
		got, err := ParseMinor(c.in, "SAR")
		if err != nil {
			t.Fatalf("ParseMinor(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseMinor(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// The scale belongs to the currency. A hardcoded 100 would overstate JPY a
// hundredfold and understate KWD tenfold, and neither shows up in a feed that
// only carries two digit currencies.
func TestParseMinorUsesCurrencyScale(t *testing.T) {
	cases := []struct {
		in       string
		currency string
		want     int64
	}{
		{"1500", "JPY", 1500},
		{"1500.4", "JPY", 1500},
		{"1500.5", "JPY", 1501},
		{"-1500.5", "JPY", -1501},
		{"12.345", "KWD", 12345},
		{"12.3455", "KWD", 12346},
		{"12.3454", "KWD", 12345},
		{"315.00", "SAR", 31500},
		{"315.00", "USD", 31500},
	}

	for _, c := range cases {
		got, err := ParseMinor(c.in, c.currency)
		if err != nil {
			t.Errorf("ParseMinor(%q, %q): %v", c.in, c.currency, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMinor(%q, %q) = %d, want %d", c.in, c.currency, got, c.want)
		}
	}
}

func TestParseMinorRejectsUnknownCurrency(t *testing.T) {
	for _, currency := range []string{"", "SAT", "XXX", "sar", "BITCOIN"} {
		_, err := ParseMinor("100.00", currency)
		if !errors.Is(err, ErrUnknownCurrency) {
			t.Errorf("ParseMinor with currency %q: error = %v, want ErrUnknownCurrency",
				currency, err)
		}
	}
}

func TestExponent(t *testing.T) {
	cases := map[string]int{"SAR": 2, "USD": 2, "EUR": 2, "JPY": 0, "KRW": 0, "KWD": 3, "BHD": 3}

	for currency, want := range cases {
		got, err := Exponent(currency)
		if err != nil {
			t.Errorf("Exponent(%q): %v", currency, err)
			continue
		}
		if got != want {
			t.Errorf("Exponent(%q) = %d, want %d", currency, got, want)
		}
	}

	if Known("SAT") {
		t.Error("SAT is not a currency")
	}
	if !Known("SAR") {
		t.Error("SAR must be known")
	}
}
