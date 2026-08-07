package pricing

import "testing"

func table() Table {
	return Table{
		Currency: "USD",
		PerHour: map[string]float64{
			"e2-medium":      0.0335,
			"e2-medium/spot": 0.0100,
			"n2-standard-4":  0.1942,
		},
	}
}

// Spot and on-demand of the same shape are different amounts of money.
// Reporting one as the other would be a lie with a decimal point in it.
func TestCapacityTypeWinsOverTheBareShape(t *testing.T) {
	tb := table()
	if got, ok := tb.Hourly("e2-medium", "spot"); !ok || got != 0.0100 {
		t.Errorf("spot = %v (%v), want 0.01", got, ok)
	}
	if got, ok := tb.Hourly("e2-medium", "on-demand"); !ok || got != 0.0335 {
		t.Errorf("on-demand fell through to the bare shape = %v (%v), want 0.0335", got, ok)
	}
	if got, ok := tb.Hourly("e2-medium", ""); !ok || got != 0.0335 {
		t.Errorf("unknown capacity type = %v (%v), want the bare shape", got, ok)
	}
}

// Unknown is not zero. Zero is a real price; conflating them lets an unpriced
// fleet report a saving of exactly nothing while looking like a measurement.
func TestUnknownIsNotZero(t *testing.T) {
	tb := table()
	if _, ok := tb.Hourly("m5.large", "on-demand"); ok {
		t.Error("an unlisted machine type reported a price")
	}
	if _, ok := tb.Hourly("", ""); ok {
		t.Error("a node with no instance type reported a price")
	}

	free := Table{Currency: "USD", PerHour: map[string]float64{"tiny": 0}}
	if v, ok := free.Hourly("tiny", ""); !ok || v != 0 {
		t.Errorf("a genuinely free machine = %v (%v); zero is a price, not an absence", v, ok)
	}
}

// The figure must always say how much of the fleet it describes.
func TestValueCountsWhatItCouldNotPrice(t *testing.T) {
	got := table().Value([]Node{
		{InstanceType: "e2-medium", CapacityType: "on-demand"},
		{InstanceType: "e2-medium", CapacityType: "spot"},
		{InstanceType: "m5.large", CapacityType: "on-demand"}, // not in the table
		{}, // kwok: unpriced, not free
	})

	if got.Priced != 2 {
		t.Errorf("priced = %d, want 2", got.Priced)
	}
	if got.Unpriced != 2 {
		t.Errorf("unpriced = %d, want 2", got.Unpriced)
	}
	want := 0.0335 + 0.0100
	if diff := got.PerHour - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("perHour = %v, want %v", got.PerHour, want)
	}
	// 730 hours, the mean month. 720 would understate by a day and a half a
	// year, which is the sort of quiet rounding a cost figure must not do.
	if diff := got.PerMonth - want*730; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("perMonth = %v, want %v", got.PerMonth, want*730)
	}
}

// An operator who has configured nothing gets capacity, never zeroes.
func TestNoTableMeansNoClaim(t *testing.T) {
	var none Table
	if !none.Empty() {
		t.Error("an unconfigured table should report itself empty")
	}
	got := none.Value([]Node{{InstanceType: "e2-medium"}})
	if got.Priced != 0 || got.Unpriced != 1 || got.PerHour != 0 {
		t.Errorf("unconfigured table produced %+v; it must claim nothing", got)
	}
}

// Helm's --set turns 0.0335 into the string "0.0335"; the same value in a
// values file stays a number. Both are the operator saying the same thing.
func TestPricesParseAsNumbersOrStrings(t *testing.T) {
	for _, body := range []string{
		`{"currency":"USD","perHour":{"e2-medium":0.0335}}`,
		`{"currency":"USD","perHour":{"e2-medium":"0.0335"}}`,
	} {
		var tb Table
		if err := tb.UnmarshalJSON([]byte(body)); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if got, ok := tb.Hourly("e2-medium", ""); !ok || got != 0.0335 {
			t.Errorf("%s → %v (%v), want 0.0335", body, got, ok)
		}
	}

	var bad Table
	if err := bad.UnmarshalJSON([]byte(`{"perHour":{"e2-medium":"free"}}`)); err == nil {
		t.Error("a non-numeric price was accepted; it must be a loud configuration error")
	}
}
