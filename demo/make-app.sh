#!/usr/bin/env bash
# Build the MergeRadar demo application: a small chai-stall billing service in Go.
#
#   make-app.sh <dir>      fresh repo on branch main, tests green
#
# Money is kept in integer paise (1 rupee = 100 paise). The pipeline is
# catalog -> Price -> Subtotal -> Discount -> GST -> Bill -> Render, plus a
# command that prints a receipt. The demo scenarios in run-agents.sh give two
# agents tasks that each pass alone, merge cleanly, and break together.
set -euo pipefail
DIR=${1:?usage: make-app.sh <dir>}
rm -rf "$DIR"; mkdir -p "$DIR/cmd/stall"; cd "$DIR"
git init -q; git checkout -q -b main 2>/dev/null || git branch -M main

cat > go.mod <<'EOF'
module example.com/stall

go 1.22
EOF

cat > catalog.go <<'EOF'
package stall

// Item is one thing the stall sells. Paise is the unit price in paise
// (1 rupee = 100 paise) so that money stays in integers.
type Item struct {
	Name     string
	Paise    int
	Category string
}

var catalog = []Item{
	{Name: "chai", Paise: 1000, Category: "drinks"},
	{Name: "coffee", Paise: 2000, Category: "drinks"},
	{Name: "samosa", Paise: 1500, Category: "snacks"},
	{Name: "vada", Paise: 1200, Category: "snacks"},
	{Name: "bun", Paise: 800, Category: "snacks"},
}

// Lookup finds an item by name.
func Lookup(name string) (Item, bool) {
	for _, it := range catalog {
		if it.Name == name {
			return it, true
		}
	}
	return Item{}, false
}

// Names lists everything on the menu, in menu order.
func Names() []string {
	out := make([]string, 0, len(catalog))
	for _, it := range catalog {
		out = append(out, it.Name)
	}
	return out
}
EOF

cat > pricing.go <<'EOF'
package stall

// Price returns the unit price of an item in paise. Unknown items cost 0.
func Price(name string) int {
	it, ok := Lookup(name)
	if !ok {
		return 0
	}
	return it.Paise
}

// Subtotal adds up Price for every item, in paise.
func Subtotal(items []string) int {
	total := 0
	for _, name := range items {
		total += Price(name)
	}
	return total
}
EOF

cat > tax.go <<'EOF'
package stall

// GSTPercent is the goods and services tax charged on the discounted subtotal.
const GSTPercent = 5

// GST returns the tax to add on top of an amount in paise, rounded down.
func GST(amountPaise int) int {
	return amountPaise * GSTPercent / 100
}
EOF

cat > discount.go <<'EOF'
package stall

// Discount returns the amount to take off a subtotal (in paise) for a coupon.
// Unknown coupons give nothing.
func Discount(subtotalPaise int, coupon string) int {
	switch coupon {
	case "CHAI10":
		return subtotalPaise * 10 / 100
	case "FLAT5":
		if subtotalPaise >= 2000 {
			return 500
		}
	}
	return 0
}
EOF

cat > format.go <<'EOF'
package stall

import "fmt"

// Money formats an amount in paise as rupees with two decimals: 1050 -> "10.50".
func Money(paise int) string {
	return fmt.Sprintf("%d.%02d", paise/100, paise%100)
}
EOF

cat > receipt.go <<'EOF'
package stall

import (
	"fmt"
	"strings"
)

// Line is one row of a receipt. Amount is in paise.
type Line struct {
	Name   string
	Amount int
}

// Receipt is a priced order. All amounts are in paise.
type Receipt struct {
	Lines    []Line
	Subtotal int
	Discount int
	Tax      int
	Total    int
}

// Bill prices an order: subtotal, coupon discount, GST on the discounted
// amount, total.
func Bill(items []string, coupon string) Receipt {
	r := Receipt{}
	for _, name := range items {
		r.Lines = append(r.Lines, Line{Name: name, Amount: Price(name)})
	}
	r.Subtotal = Subtotal(items)
	r.Discount = Discount(r.Subtotal, coupon)
	taxable := r.Subtotal - r.Discount
	r.Tax = GST(taxable)
	r.Total = taxable + r.Tax
	return r
}

// Render prints a receipt the way the thermal printer does.
func Render(r Receipt) string {
	var b strings.Builder
	b.WriteString("       CHAI STALL\n")
	b.WriteString("-----------------------\n")
	for _, l := range r.Lines {
		fmt.Fprintf(&b, "%-12s %10s\n", l.Name, Money(l.Amount))
	}
	b.WriteString("-----------------------\n")
	fmt.Fprintf(&b, "%-12s %10s\n", "subtotal", Money(r.Subtotal))
	if r.Discount > 0 {
		fmt.Fprintf(&b, "%-12s %10s\n", "discount", "-"+Money(r.Discount))
	}
	fmt.Fprintf(&b, "%-12s %10s\n", fmt.Sprintf("GST %d%%", GSTPercent), Money(r.Tax))
	fmt.Fprintf(&b, "%-12s %10s\n", "TOTAL", Money(r.Total))
	return b.String()
}
EOF

cat > pricing_test.go <<'EOF'
package stall

import "testing"

func TestPriceIsInPaise(t *testing.T) {
	if got := Price("chai"); got != 1000 {
		t.Fatalf("Price(chai) = %d paise, want 1000", got)
	}
	if got := Price("nothing"); got != 0 {
		t.Fatalf("unknown item should cost 0, got %d", got)
	}
}

func TestSubtotalAddsEveryItem(t *testing.T) {
	if got := Subtotal([]string{"chai", "samosa", "coffee"}); got != 4500 {
		t.Fatalf("Subtotal = %d, want 4500", got)
	}
}
EOF

cat > format_test.go <<'EOF'
package stall

import "testing"

func TestMoneyFormatsPaiseAsRupees(t *testing.T) {
	cases := map[int]string{1050: "10.50", 5: "0.05", 123456: "1234.56"}
	for paise, want := range cases {
		if got := Money(paise); got != want {
			t.Fatalf("Money(%d) = %q, want %q", paise, got, want)
		}
	}
}
EOF

cat > receipt_test.go <<'EOF'
package stall

import (
	"strings"
	"testing"
)

func TestBillAddsGSTAfterDiscount(t *testing.T) {
	r := Bill([]string{"chai", "samosa", "coffee"}, "CHAI10")
	if r.Subtotal != 4500 || r.Discount != 450 || r.Tax != 202 || r.Total != 4252 {
		t.Fatalf("bill = %+v", r)
	}
}

func TestRenderEndsWithTheTotal(t *testing.T) {
	out := Render(Bill([]string{"chai", "samosa"}, ""))
	if !strings.HasSuffix(strings.TrimSpace(out), "26.25") {
		t.Fatalf("receipt should end with the total 26.25:\n%s", out)
	}
}
EOF

cat > cmd/stall/main.go <<'EOF'
// Command stall prints a receipt for the items named on the command line.
//
//	go run ./cmd/stall chai samosa
//	go run ./cmd/stall -coupon CHAI10 chai samosa coffee
package main

import (
	"flag"
	"fmt"

	"example.com/stall"
)

func main() {
	coupon := flag.String("coupon", "", "coupon code")
	flag.Parse()
	items := flag.Args()
	if len(items) == 0 {
		items = []string{"chai", "samosa"}
	}
	fmt.Print(stall.Render(stall.Bill(items, *coupon)))
}
EOF

gofmt -l . | grep . && { echo "gofmt complaints above" >&2; exit 1; } || true
go vet ./... && go test ./... >/dev/null
git add . && git commit -q -m "chai stall billing: catalog, pricing in paise, GST, discounts, receipt"
echo "app ready at $DIR ($(git ls-files | wc -l | tr -d ' ') files, $(cat *.go cmd/stall/*.go | wc -l | tr -d ' ') lines, tests green)"
