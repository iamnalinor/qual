#!/usr/bin/env bash
# Create the deterministic live/demo fixture. The committed test catches the seed.
set -euo pipefail
if [[ $# != 1 || $1 != /* || -e $1 ]]; then
  echo 'Usage: create-fixture.sh /absolute/new-directory' >&2
  exit 1
fi
mkdir -m 700 "$1"
cd "$1"
cat > go.mod <<'GO'
module fixture.example/divide

go 1.23.0
GO
cat > divide.go <<'GO'
package fixture

// Divide returns zero when the divisor is zero.
func Divide(a, b int) int {
	if b == 0 {
		return 0
	}
	return a / b
}
GO
cat > divide_test.go <<'GO'
package fixture

import "testing"

func TestDivide(t *testing.T) {
	if got := Divide(8, 2); got != 4 {
		t.Fatalf("Divide(8, 2) = %d; want 4", got)
	}
}

func TestDivideZero(t *testing.T) {
	if got := Divide(8, 0); got != 0 {
		t.Fatalf("Divide(8, 0) = %d; want 0", got)
	}
}
GO
git init -q
git add go.mod divide.go divide_test.go
git -c user.name='Qual fixture' -c user.email=fixture@example.test commit -qm 'Handle zero divisor'
cat > divide.go <<'GO'
package fixture

// Divide returns zero when the divisor is zero.
func Divide(a, b int) int {
	return a / b
}
GO
