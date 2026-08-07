// Package pricing turns reclaimed capacity into money, when — and only when —
// an operator has said what their machines cost.
//
// There is no built-in price table and there will not be one. List prices vary
// by region, change without notice, and are simply wrong for anyone with a
// committed-use discount, a savings plan or an enterprise agreement. A
// consolidation tool whose headline figure is a plausible guess is the thing
// this product refuses to be: the packing ceiling had to become real before the
// UI was allowed to draw it, and a price is the same kind of claim.
//
// So an unpriced machine type is reported as unpriced. The capacity is still
// measured, still shown, and still true; only the currency is withheld, which
// is the honest half of the answer rather than a missing one.
package pricing

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Table maps a machine shape to its hourly cost, in the operator's currency.
//
// Keys are matched most specific first: "e2-medium/spot" before "e2-medium",
// because a spot node and an on-demand node of the same shape are different
// amounts of money and reporting one as the other would be a lie with a
// decimal point in it.
type Table struct {
	Currency string
	PerHour  map[string]float64
}

// UnmarshalJSON accepts prices as numbers or as strings.
//
// Helm's --set turns 0.0335 into the string "0.0335", while the same value in
// a values file stays a number. Both are the operator saying the same thing,
// and rejecting one of them would drop pricing silently for anyone who
// configured it the first way — a failure they would only notice as an
// absence.
func (t *Table) UnmarshalJSON(b []byte) error {
	var raw struct {
		Currency string                     `json:"currency"`
		PerHour  map[string]json.RawMessage `json:"perHour"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	t.Currency = raw.Currency
	if len(raw.PerHour) == 0 {
		return nil
	}
	t.PerHour = make(map[string]float64, len(raw.PerHour))
	for k, v := range raw.PerHour {
		s := strings.Trim(string(v), `"`)
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("price for %q is not a number: %s", k, s)
		}
		t.PerHour[k] = f
	}
	return nil
}

// Empty reports whether anything is priced at all. An operator who has not
// configured pricing gets capacity, not zeroes.
func (t Table) Empty() bool { return len(t.PerHour) == 0 }

// Hourly returns the cost of one node of this shape, and whether it is known.
//
// Unknown is not zero. Zero is a real price — a node that costs nothing —
// and conflating the two would let an unpriced fleet report a saving of
// exactly nothing while looking like a measurement.
func (t Table) Hourly(instanceType, capacityType string) (float64, bool) {
	if instanceType == "" {
		return 0, false
	}
	if capacityType != "" {
		if v, ok := t.PerHour[instanceType+"/"+strings.ToLower(capacityType)]; ok {
			return v, true
		}
	}
	v, ok := t.PerHour[instanceType]
	return v, ok
}

// Reclaimed is what a set of reclaimed nodes is worth.
type Reclaimed struct {
	// PerHour is the rate no longer being spent. This is the result: a
	// reclaimed node saves a rate, forever, not a one-off amount.
	PerHour float64
	// PerMonth is the same figure at 730 hours, because nobody budgets by the
	// hour and everyone recognises a monthly number.
	PerMonth float64
	// Priced and Unpriced count nodes, so the figure above can always say how
	// much of the fleet it actually describes.
	Priced   int
	Unpriced int
	Currency string
}

// Node describes one reclaimed machine to price.
type Node struct {
	InstanceType string
	CapacityType string
}

// Value prices a set of reclaimed nodes.
func (t Table) Value(nodes []Node) Reclaimed {
	out := Reclaimed{Currency: t.Currency}
	for _, n := range nodes {
		rate, ok := t.Hourly(n.InstanceType, n.CapacityType)
		if !ok {
			out.Unpriced++
			continue
		}
		out.Priced++
		out.PerHour += rate
	}
	// 730 is the mean hours in a month. Using 720 (30 days) would understate
	// by a day and a half a year, which is exactly the sort of quiet rounding
	// this product should not do to a cost figure.
	out.PerMonth = out.PerHour * 730
	return out
}
