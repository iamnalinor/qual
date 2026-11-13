package live_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"testing"

	"github.com/iamnalinor/qual/internal/budget"
	"github.com/iamnalinor/qual/internal/domain"
)

func ledgerEvent(t *testing.T, sequence int64, state budget.State) domain.Event {
	t.Helper()
	data, err := json.Marshal(struct {
		Ledger budget.State `json:"ledger"`
	}{state})
	if err != nil {
		t.Fatal(err)
	}
	return domain.Event{Sequence: sequence, AgentID: "reviewer", Type: "agent_checkpoint", Data: data}
}

func TestDurableResumeRejectsResetAndLostReservations(t *testing.T) {
	prior := budget.State{Spent: 100, Reserved: 30, Next: 3, Unknown: 1, Reservations: map[uint64]domain.Money{2: 30}}
	before := []domain.Event{ledgerEvent(t, 1, prior)}
	reset := prior
	reset.Next = 0
	reset.Reservations = nil
	if err := validateResumeLedgers(before, append(append([]domain.Event(nil), before...), ledgerEvent(t, 2, reset))); err == nil {
		t.Fatal("reset ledger passed")
	}
	lost := prior
	lost.Reserved = 0
	lost.Reservations = nil
	if err := validateResumeLedgers(before, append(append([]domain.Event(nil), before...), ledgerEvent(t, 2, lost))); err == nil {
		t.Fatal("lost outstanding reservation passed")
	}
	reused := prior
	reused.Next = 4
	reused.Spent = 200
	if err := validateResumeLedgers(before, append(append([]domain.Event(nil), before...), ledgerEvent(t, 2, reused))); err == nil {
		t.Fatal("first resumed checkpoint did not restore exact ledger")
	}
	good := append(append([]domain.Event(nil), before...), ledgerEvent(t, 2, prior))
	next := prior
	next.Spent = 200
	next.Next = 4
	good = append(good, ledgerEvent(t, 3, next))
	if err := validateResumeLedgers(before, good); err != nil {
		t.Fatal(err)
	}
	if err := validateResumeLedgers(before, append(append([]domain.Event(nil), good...), ledgerEvent(t, 4, lost))); err == nil {
		t.Fatal("outstanding reservation disappeared after restoration")
	}
	tampered := append([]domain.Event(nil), good...)
	tampered[0] = ledgerEvent(t, 1, next)
	if err := validateResumeLedgers(before, tampered); err == nil {
		t.Fatal("changed historical attempt state passed")
	}
}

func TestDurableResumeRejectsRegressionBetweenResumedCheckpoints(t *testing.T) {
	prior := budget.State{Spent: 100, Next: 3, CostKnown: true}
	before := []domain.Event{ledgerEvent(t, 1, prior)}
	advanced := budget.State{Spent: 200, Next: 4, Unknown: 1, CostKnown: false}
	for _, field := range []string{"spent", "next", "unknown", "cost_known"} {
		t.Run(field, func(t *testing.T) {
			regressed := advanced
			switch field {
			case "spent":
				regressed.Spent = 100
			case "next":
				regressed.Next = 3
			case "unknown":
				regressed.Unknown = 0
			case "cost_known":
				regressed.CostKnown = true
			}
			after := append(append([]domain.Event(nil), before...), ledgerEvent(t, 2, prior), ledgerEvent(t, 3, advanced), ledgerEvent(t, 4, regressed))
			if err := validateResumeLedgers(before, after); err == nil {
				t.Fatal("regression after resumed progress passed")
			}
		})
	}
}

func TestDurableResumeAllowsNewReservationSettlement(t *testing.T) {
	prior := budget.State{Spent: 100, Reserved: 30, Next: 3, Unknown: 1, Reservations: map[uint64]domain.Money{2: 30}}
	before := []domain.Event{ledgerEvent(t, 1, prior)}
	reserved := budget.State{Spent: 100, Reserved: 80, Next: 4, Unknown: 1, Reservations: map[uint64]domain.Money{2: 30, 4: 50}}
	settled := budget.State{Spent: 120, Reserved: 30, Next: 4, Unknown: 1, Reservations: map[uint64]domain.Money{2: 30}}
	after := append(append([]domain.Event(nil), before...), ledgerEvent(t, 2, prior), ledgerEvent(t, 3, reserved), ledgerEvent(t, 4, settled))
	if err := validateResumeLedgers(before, after); err != nil {
		t.Fatal(err)
	}
}

func TestDurableResumeRequiresEveryActiveRoleRestoration(t *testing.T) {
	prior := budget.State{Spent: 100, Next: 3, CostKnown: true}
	reviewer := ledgerEvent(t, 1, prior)
	leader := ledgerEvent(t, 2, prior)
	leader.AgentID = "leader"
	before := []domain.Event{reviewer, leader}
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprintf("one_role_restored_%t", restored), func(t *testing.T) {
			after := append([]domain.Event(nil), before...)
			if restored {
				after = append(after, ledgerEvent(t, 3, prior))
			}
			if err := validateResumeLedgers(before, after); err == nil {
				t.Fatal("missing active role restoration passed")
			}
		})
	}
	restoredReviewer := ledgerEvent(t, 3, prior)
	restoredLeader := ledgerEvent(t, 4, prior)
	restoredLeader.AgentID = "leader"
	if err := validateResumeLedgers(before, append(before, restoredReviewer, restoredLeader)); err != nil {
		t.Fatal(err)
	}
}

func TestDurableResumeHonorsRuntimeFinishedRoleProtocol(t *testing.T) {
	for _, eventType := range []string{"agent_checkpoint", "tool_result"} {
		for _, status := range []domain.Status{domain.StatusCompleted, domain.StatusBudgetStopped, domain.StatusCancelled} {
			t.Run(eventType+"/"+string(status), func(t *testing.T) {
				prior := budget.State{Spent: 100, Next: 3, CostKnown: true}
				before := []domain.Event{ledgerEvent(t, 1, prior)}
				data, err := json.Marshal(struct {
					Ledger   budget.State        `json:"ledger"`
					Finished *domain.AgentResult `json:"finished,omitempty"`
				}{prior, &domain.AgentResult{AgentID: "reviewer", Status: status}})
				if err != nil {
					t.Fatal(err)
				}
				before = append(before, domain.Event{Sequence: 2, AgentID: "reviewer", Type: eventType, Data: data})
				err = validateResumeLedgers(before, before)
				wantRestoration := status == domain.StatusCancelled
				if (err != nil) != wantRestoration {
					t.Fatalf("restoration required = %t, error = %v", wantRestoration, err)
				}
			})
		}
	}
}

func TestDurableResumeAllowsRecoveryMarkingOutstandingUsageUnknown(t *testing.T) {
	prior := budget.State{Spent: 100, Reserved: 30, Next: 3, CostKnown: true, Reservations: map[uint64]domain.Money{3: 30}}
	before := []domain.Event{ledgerEvent(t, 1, prior)}
	recovered := prior
	recovered.CostKnown = false
	recovered.Unknown = 1
	after := append(append([]domain.Event(nil), before...), ledgerEvent(t, 2, recovered))
	if err := validateResumeLedgers(before, after); err != nil {
		t.Fatal(err)
	}
}

func validateResumeLedgers(before, after []domain.Event) error {
	if len(after) < len(before) || !reflect.DeepEqual(before, after[:len(before)]) {
		return errors.New("resume changed durable history and prior attempt identities")
	}
	prior, err := priorRoleLedgers(before)
	if err != nil {
		return err
	}
	restored := map[domain.AgentID]bool{}
	previous := maps.Clone(prior)
	for _, event := range after[len(before):] {
		baseline, exists := prior[event.AgentID]
		if event.Type != "agent_checkpoint" || !exists {
			continue
		}
		state, err := checkpointLedger(event)
		if err != nil {
			return err
		}
		if err = validateResumedCheckpoint(baseline, previous[event.AgentID], state, restored[event.AgentID]); err != nil {
			return fmt.Errorf("role %s: %w", event.AgentID, err)
		}
		restored[event.AgentID] = true
		previous[event.AgentID] = state
	}
	for id := range prior {
		if !restored[id] {
			return fmt.Errorf("role %s missing restored ledger checkpoint", id)
		}
	}
	return nil
}

func validateResumedCheckpoint(baseline, previous, state budget.State, restored bool) error {
	if !restored && !reflect.DeepEqual(baseline, state) {
		return errors.New("did not restore exact durable ledger")
	}
	if err := retainsLedger(baseline, state); err != nil {
		return err
	}
	return monotonicLedger(previous, state)
}

func checkpointLedger(event domain.Event) (budget.State, error) {
	var checkpoint struct {
		Ledger *budget.State `json:"ledger"`
	}
	if err := json.Unmarshal(event.Data, &checkpoint); err != nil {
		return budget.State{}, err
	}
	if checkpoint.Ledger == nil {
		return budget.State{}, errors.New("checkpoint missing durable ledger")
	}
	return *checkpoint.Ledger, nil
}

func monotonicLedger(before, after budget.State) error {
	if before.Limits != after.Limits || after.Spent < before.Spent || after.Next < before.Next || after.Unknown < before.Unknown {
		return errors.New("resume reset durable ledger counters or limits")
	}
	if !before.CostKnown && after.CostKnown {
		return errors.New("resume erased unknown cost")
	}
	return nil
}

func retainsLedger(before, after budget.State) error {
	if err := monotonicLedger(before, after); err != nil {
		return err
	}
	if after.Reserved < before.Reserved {
		return errors.New("resume lost prior reserved balance")
	}
	for id, amount := range before.Reservations {
		if retained, exists := after.Reservations[id]; !exists || retained != amount {
			return fmt.Errorf("resume lost prior outstanding reservation %d", id)
		}
	}
	return nil
}

func priorRoleLedgers(before []domain.Event) (map[domain.AgentID]budget.State, error) {
	prior := map[domain.AgentID]budget.State{}
	finished := map[domain.AgentID]*domain.AgentResult{}
	for _, event := range before {
		if event.Type != "agent_checkpoint" && event.Type != "tool_result" {
			continue
		}
		var progress struct {
			Finished *domain.AgentResult `json:"finished"`
		}
		if err := json.Unmarshal(event.Data, &progress); err != nil {
			return nil, err
		}
		if event.Type == "agent_checkpoint" {
			state, err := checkpointLedger(event)
			if err != nil {
				return nil, err
			}
			prior[event.AgentID] = state
			finished[event.AgentID] = progress.Finished
		} else if progress.Finished != nil {
			finished[event.AgentID] = progress.Finished
		}
	}
	if len(prior) == 0 {
		return nil, errors.New("no durable role ledgers before resume")
	}
	return recoverActiveLedgers(prior, finished), nil
}

func recoverActiveLedgers(prior map[domain.AgentID]budget.State, finished map[domain.AgentID]*domain.AgentResult) map[domain.AgentID]budget.State {
	// Runtime recovery resumes cancelled roles and skips other finished roles,
	// including finishes persisted in tool results before the next checkpoint.
	for id, result := range finished {
		if result != nil && result.Status != domain.StatusCancelled {
			delete(prior, id)
		}
	}
	// Recovery conservatively marks any in-flight reservation unknown before
	// initialize writes the first resumed checkpoint.
	for id, state := range prior {
		if len(state.Reservations) > 0 {
			state.CostKnown = false
			state.Unknown = len(state.Reservations)
			prior[id] = state
		}
	}
	return prior
}
