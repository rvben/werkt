package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestInvestigationDispatcherHandoffWakesListener(t *testing.T) {
	url := os.Getenv("WERKT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WERKT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	writer, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(context.Background()) //nolint:errcheck
	listener, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close(context.Background()) //nolint:errcheck
	raw, err := os.ReadFile("../../deploy/investigator/outbox.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Load the deployed function body, isolating only its schema and trigger
	// table. No production runs or investigations are created or modified.
	_, function, found := strings.Cut(string(raw), "CREATE OR REPLACE FUNCTION investigation_dispatch.wake_on_review_completion()")
	if !found {
		t.Fatal("dispatcher wakeup function is missing")
	}
	function, _, found = strings.Cut(function, "REVOKE ALL ON FUNCTION")
	if !found {
		t.Fatal("dispatcher wakeup function terminator is missing")
	}
	schema := pgx.Identifier{fmt.Sprintf("dispatcher_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := writer.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer writer.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") //nolint:errcheck
	setup := "CREATE FUNCTION " + schema + ".wake_on_review_completion()" + function + `
CREATE TEMP TABLE dispatcher_wakeup_fixture (state jsonb);
CREATE TRIGGER dispatcher_wakeup_fixture AFTER UPDATE OF state ON dispatcher_wakeup_fixture
FOR EACH ROW EXECUTE FUNCTION ` + schema + `.wake_on_review_completion();
INSERT INTO dispatcher_wakeup_fixture VALUES ('{"status":"running"}');`
	if _, err := writer.Exec(ctx, setup); err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Exec(ctx, "LISTEN werkt_investigation_dispatch"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"review", "local", "completed", "abandoned"} {
		t.Run(status, func(t *testing.T) {
			if _, err := writer.Exec(ctx, "UPDATE dispatcher_wakeup_fixture SET state = jsonb_build_object('status', $1::text)", status); err != nil {
				t.Fatal(err)
			}
			wait, stop := context.WithTimeout(ctx, time.Second)
			notification, err := listener.WaitForNotification(wait)
			stop()
			if err != nil {
				t.Fatalf("committed %s transition did not wake listener: %v", status, err)
			}
			if notification.Channel != "werkt_investigation_dispatch" || notification.Payload != "" {
				t.Fatalf("unexpected notification: %#v", notification)
			}
			if _, err := writer.Exec(ctx, "UPDATE dispatcher_wakeup_fixture SET state = state"); err != nil {
				t.Fatal(err)
			}
			wait, stop = context.WithTimeout(ctx, 100*time.Millisecond)
			_, err = listener.WaitForNotification(wait)
			stop()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unchanged observation should not notify: %v", err)
			}
			if _, err := writer.Exec(ctx, `UPDATE dispatcher_wakeup_fixture SET state = '{"status":"running"}'`); err != nil {
				t.Fatal(err)
			}
		})
	}
}
