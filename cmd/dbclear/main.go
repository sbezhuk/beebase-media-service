// Command dbclear permanently deletes all application data from the
// service's database while leaving the schema (tables, indexes,
// constraints, migrations) intact. It reads DATABASE_URL directly rather
// than going through internal/config, so it doesn't require unrelated
// service configuration (JWKS URLs, peer service URLs, ...) to be set.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/joho/godotenv"

	"github.com/sbezhuk/beebase-media-service/internal/platform/postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dbclear:", err)
		os.Exit(1)
	}
}

func run() error {
	yes := flag.Bool("yes", false, "skip the confirmation prompt")
	flag.Parse()

	// .env is optional: present in local dev, absent in production/containers.
	_ = godotenv.Load()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	if !*yes && !confirm(dsn) {
		fmt.Println("aborted")
		return nil
	}

	ctx := context.Background()
	pool, err := postgres.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	cleared, err := postgres.ClearAllData(ctx, pool)
	if err != nil {
		return fmt.Errorf("clear data: %w", err)
	}

	if len(cleared) == 0 {
		fmt.Println("no application tables found; nothing to clear")
		return nil
	}

	fmt.Printf("cleared %d table(s): %s\n", len(cleared), strings.Join(cleared, ", "))
	return nil
}

// confirm prompts on stdin before touching dsn's target database. The DSN
// is printed with credentials stripped so it's still identifiable without
// leaking a password to the terminal or shell history/logs.
func confirm(dsn string) bool {
	fmt.Printf("This will permanently delete ALL data in %s\n", redact(dsn))
	fmt.Print("Type 'yes' to continue: ")

	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(answer) == "yes"
}

func redact(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "<database>"
	}
	u.User = nil
	return u.String()
}
