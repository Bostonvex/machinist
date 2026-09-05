package controlplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func claimStore(t *testing.T) *Store {
	t.Helper()
	return openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
}

// take claims an issue for a holder, for a window that has not run out.
func take(t *testing.T, store *Store, holder string, window time.Duration) (Claim, error) {
	t.Helper()
	return store.TakeClaim(t.Context(), Claim{
		Repository: "Bostonvex/machinist", Issue: 7, Holder: holder,
		Branch: "feat/" + holder, Reason: "working on it",
		ExpiresAt: time.Now().Add(window),
	})
}

func mustTake(t *testing.T, store *Store, holder string, window time.Duration) Claim {
	t.Helper()
	claim, err := take(t, store, holder, window)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestAClaimedIssueIsRefusedToAnyoneElse(t *testing.T) {
	store := claimStore(t)
	mustTake(t, store, "seat-a", time.Hour)
	_, err := take(t, store, "seat-b", time.Hour)
	var taken *ErrClaimTaken
	if !errors.As(err, &taken) {
		t.Fatalf("second claim = %v, want a refusal naming the holder", err)
	}
	if taken.Holder != "seat-a" {
		t.Fatalf("refusal named %q, want the seat that actually holds it", taken.Holder)
	}
}

func TestAHolderMayExtendTheirOwnClaimWithoutLosingWhenItStarted(t *testing.T) {
	store := claimStore(t)
	first := mustTake(t, store, "seat-a", time.Hour)
	second := mustTake(t, store, "seat-a", 4*time.Hour)
	if !second.ClaimedAt.Equal(first.ClaimedAt) {
		t.Fatalf("claimed_at moved from %s to %s: extending a claim must not reset how long the work has been going",
			first.ClaimedAt, second.ClaimedAt)
	}
	if !second.ExpiresAt.After(first.ExpiresAt) {
		t.Fatalf("expiry did not extend: %s then %s", first.ExpiresAt, second.ExpiresAt)
	}
}

func TestAnExpiredClaimStopsNobody(t *testing.T) {
	store := claimStore(t)
	if _, err := store.TakeClaim(t.Context(), Claim{
		Repository: "Bostonvex/machinist", Issue: 7, Holder: "seat-a",
		Reason: "working on it", ExpiresAt: time.Now().Add(time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	claim, err := take(t, store, "seat-b", time.Hour)
	if err != nil {
		t.Fatalf("a lapsed claim blocked a new one: %v", err)
	}
	if claim.Holder != "seat-b" {
		t.Fatalf("holder = %q, want the seat that took it", claim.Holder)
	}
}

func TestAClaimWithNoEndIsRefused(t *testing.T) {
	store := claimStore(t)
	_, err := store.TakeClaim(t.Context(), Claim{
		Repository: "Bostonvex/machinist", Issue: 7, Holder: "seat-a", Reason: "working on it",
	})
	if !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("err = %v, want an invalid claim: a claim nobody can outlast is a lock", err)
	}
	// Supplying no expiry and supplying one that has passed are different
	// mistakes, and a caller who supplied nothing is not helped by being told
	// their claim has already expired. Asserting the message keeps them
	// separate; without it the past-expiry check silently covers both and the
	// missing-expiry one could be deleted unnoticed.
	if !strings.Contains(err.Error(), "needs an expiry") {
		t.Fatalf("err = %q, want it to say the claim needs an expiry rather than that it already expired", err)
	}
}

func TestAClaimThatHasAlreadyExpiredIsRefused(t *testing.T) {
	store := claimStore(t)
	_, err := store.TakeClaim(t.Context(), Claim{
		Repository: "Bostonvex/machinist", Issue: 7, Holder: "seat-a", Reason: "working on it",
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("err = %v, want an invalid claim", err)
	}
}

func TestEveryFieldThatMakesAClaimReadableIsRequired(t *testing.T) {
	store := claimStore(t)
	future := time.Now().Add(time.Hour)
	for name, claim := range map[string]Claim{
		"no repository": {Issue: 7, Holder: "seat-a", Reason: "why", ExpiresAt: future},
		"no issue":      {Repository: "Bostonvex/machinist", Holder: "seat-a", Reason: "why", ExpiresAt: future},
		"issue zero":    {Repository: "Bostonvex/machinist", Issue: 0, Holder: "seat-a", Reason: "why", ExpiresAt: future},
		"no holder":     {Repository: "Bostonvex/machinist", Issue: 7, Reason: "why", ExpiresAt: future},
		"no reason":     {Repository: "Bostonvex/machinist", Issue: 7, Holder: "seat-a", ExpiresAt: future},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.TakeClaim(t.Context(), claim); !errors.Is(err, ErrInvalidClaim) {
				t.Fatalf("err = %v, want an invalid claim", err)
			}
		})
	}
}

func TestReleasingSomebodyElsesClaimIsRefused(t *testing.T) {
	store := claimStore(t)
	mustTake(t, store, "seat-a", time.Hour)
	_, err := store.ReleaseClaim(t.Context(), "Bostonvex/machinist", 7, "seat-b", "I thought it was mine")
	var taken *ErrClaimTaken
	if !errors.As(err, &taken) {
		t.Fatalf("err = %v, want a refusal: releasing another seat's claim is how real work gets lost", err)
	}
	claim, found, err := store.Claim(t.Context(), "Bostonvex/machinist", 7)
	if err != nil {
		t.Fatal(err)
	}
	if !found || claim.Holder != "seat-a" {
		t.Fatalf("claim after the refused release = %#v (found %v), want it untouched", claim, found)
	}
}

func TestReleasingAnIssueNobodyHoldsIsRefused(t *testing.T) {
	store := claimStore(t)
	_, err := store.ReleaseClaim(t.Context(), "Bostonvex/machinist", 7, "seat-a", "done")
	var missing *ErrNoClaim
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want a refusal: a release with nothing to release means the caller is reading stale state", err)
	}
}

func TestReleasingAnIssueLeavesNoRowBehind(t *testing.T) {
	store := claimStore(t)
	mustTake(t, store, "seat-a", time.Hour)
	if _, err := store.ReleaseClaim(t.Context(), "Bostonvex/machinist", 7, "seat-a", "handed back"); err != nil {
		t.Fatal(err)
	}
	_, found, err := store.Claim(t.Context(), "Bostonvex/machinist", 7)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("a released claim left a row behind, which every reader then has to remember to discount")
	}
	if _, err := take(t, store, "seat-b", time.Hour); err != nil {
		t.Fatalf("the issue was not free after release: %v", err)
	}
}

func TestAHeldIssueIsStillNotFreeWork(t *testing.T) {
	store := claimStore(t)
	mustTake(t, store, "seat-a", time.Hour)
	held, err := store.HoldClaim(t.Context(), "Bostonvex/machinist", 7, "seat-a",
		"redirected to the incident", time.Now().Add(2*time.Hour), "Bostonvex/machinist#9")
	if err != nil {
		t.Fatal(err)
	}
	if held.State != ClaimOnHold {
		t.Fatalf("state = %q, want %q", held.State, ClaimOnHold)
	}
	if held.Transfer != "Bostonvex/machinist#9" {
		t.Fatalf("transfer = %q, want where the work went", held.Transfer)
	}
	if _, err := take(t, store, "seat-b", time.Hour); err == nil {
		t.Fatal("a held issue was taken: a hold that does not stop anyone is a release with extra words")
	}
}

func TestAHoldWithNoFutureEndIsRefusedBecauseItIsAReleaseInDisguise(t *testing.T) {
	store := claimStore(t)
	mustTake(t, store, "seat-a", time.Hour)
	for name, until := range map[string]time.Time{
		"no end":   {},
		"the past": time.Now().Add(-time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.HoldClaim(t.Context(), "Bostonvex/machinist", 7, "seat-a", "redirect", until, "")
			if !errors.Is(err, ErrInvalidClaim) {
				t.Fatalf("err = %v, want an invalid claim", err)
			}
		})
	}
}

func TestATransitionNeedsAReasonBecauseItIsTheRecordOfWhyWorkStopped(t *testing.T) {
	store := claimStore(t)
	mustTake(t, store, "seat-a", time.Hour)
	if _, err := store.ReleaseClaim(t.Context(), "Bostonvex/machinist", 7, "seat-a", "  "); !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("err = %v, want an invalid claim", err)
	}
}

func TestAClaimStateTheBuildCannotReadIsAnErrorNotFreeWork(t *testing.T) {
	store := claimStore(t)
	mustTake(t, store, "seat-a", time.Hour)
	if _, err := store.db.ExecContext(t.Context(),
		`UPDATE issue_claims SET state='marinating' WHERE issue=7`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Claim(t.Context(), "Bostonvex/machinist", 7); err == nil {
		t.Fatal("an unreadable claim state read back cleanly; it must never resolve to nobody holds this")
	}
	if _, err := take(t, store, "seat-b", time.Hour); err == nil {
		t.Fatal("an issue whose claim could not be read was handed to a second seat")
	}
}

func TestAClaimExpiryTheBuildCannotReadIsAnErrorNotFreeWork(t *testing.T) {
	store := claimStore(t)
	mustTake(t, store, "seat-a", time.Hour)
	if _, err := store.db.ExecContext(t.Context(),
		`UPDATE issue_claims SET expires_at='soon' WHERE issue=7`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Claim(t.Context(), "Bostonvex/machinist", 7); err == nil {
		t.Fatal("an unreadable expiry read back cleanly")
	}
	if _, err := take(t, store, "seat-b", time.Hour); err == nil {
		t.Fatal("an issue whose expiry could not be read was handed to a second seat")
	}
}

func TestAnExpiredClaimIsStillListed(t *testing.T) {
	store := claimStore(t)
	if _, err := store.TakeClaim(t.Context(), Claim{
		Repository: "Bostonvex/machinist", Issue: 7, Holder: "seat-a",
		Reason: "working on it", ExpiresAt: time.Now().Add(time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	claims, err := store.Claims(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("claims = %d, want the lapsed one still listed: it is the trace of work that stopped without being handed back", len(claims))
	}
	if claims[0].Live(time.Now()) {
		t.Fatal("a lapsed claim listed as live")
	}
}

func TestParseClaimStateNamesWhatItWouldAccept(t *testing.T) {
	_, err := ParseClaimState("marinating")
	if err == nil {
		t.Fatal("an unknown claim state was accepted")
	}
	for _, state := range claimStates {
		if !bytes.Contains([]byte(err.Error()), []byte(state)) {
			t.Fatalf("error %q does not name %q, so it cannot tell the caller what to write", err, state)
		}
	}
}

func claimServer(t *testing.T) (*httptest.Server, map[string]string, *Store) {
	t.Helper()
	return leaseServerWithStore(t)
}

func postClaim(t *testing.T, web *httptest.Server, headers map[string]string, body map[string]any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, web.URL+"/api/v1/claims", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := web.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestAClaimCanBeTakenAndReadBackOverTheAPI(t *testing.T) {
	web, headers, _ := claimServer(t)
	response := postClaim(t, web, headers, map[string]any{
		"action": "take", "repository": "Bostonvex/machinist", "issue": 7,
		"holder": "seat-a", "reason": "working on it",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	listing := struct {
		Claims []struct {
			Holder string `json:"holder"`
			Live   bool   `json:"live"`
		} `json:"claims"`
	}{}
	read, err := http.Get(web.URL + "/api/v1/claims")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Body.Close()
	if read.StatusCode != http.StatusOK {
		t.Fatalf("listing status = %d, want 200", read.StatusCode)
	}
	if err := json.NewDecoder(read.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Claims) != 1 || listing.Claims[0].Holder != "seat-a" {
		t.Fatalf("listing = %#v", listing)
	}
	if !listing.Claims[0].Live {
		t.Fatal("a claim that has just been taken listed as not live")
	}
}

func TestAClaimAlreadyHeldIsRefusedAsAConflictNotAsABadRequest(t *testing.T) {
	web, headers, store := claimServer(t)
	mustTake(t, store, "seat-a", time.Hour)
	response := postClaim(t, web, headers, map[string]any{
		"action": "take", "repository": "Bostonvex/machinist", "issue": 7,
		"holder": "seat-b", "reason": "I want it",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	// 400 would tell seat-b to fix their request. There is nothing wrong with
	// their request; somebody else has the work.
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.StatusCode)
	}
}

func TestAnUnknownClaimActionIsRefusedRatherThanGuessed(t *testing.T) {
	web, headers, _ := claimServer(t)
	response := postClaim(t, web, headers, map[string]any{
		"action": "relinquish", "repository": "Bostonvex/machinist", "issue": 7,
		"holder": "seat-a", "reason": "done",
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: guessing the transition is guessing about taking work away from someone", response.StatusCode)
	}
}

func TestWritingAClaimNeedsMoreThanReachingThePort(t *testing.T) {
	web, _, _ := claimServer(t)
	response := postClaim(t, web, nil, map[string]any{
		"action": "take", "repository": "Bostonvex/machinist", "issue": 7,
		"holder": "seat-a", "reason": "working on it",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}
