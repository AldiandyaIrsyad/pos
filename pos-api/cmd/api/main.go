package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	// .env is optional: in docker compose the variables come from the
	// environment. Load never overrides variables that are already set.
	// A missing file is fine; a malformed one is not.
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Fatalf("load .env: %v", err)
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set (copy .env.example to .env)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// pgxpool.New only parses the config; Ping opens a real connection.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("parse db config: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("db not reachable: %v", err)
	}
	log.Println("db connected")

	var outlets, products, transactions int
	err = pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM outlets),
		       (SELECT count(*) FROM products),
		       (SELECT count(*) FROM transactions)`).
		Scan(&outlets, &products, &transactions)
	if err != nil {
		log.Fatalf("count rows: %v", err)
	}
	log.Printf("outlets=%d products=%d transactions=%d", outlets, products, transactions)

	rows, err := pool.Query(ctx, `
		SELECT p.id, p.sku, p.name, p.price, s.qty
		FROM products p
		JOIN stocks s ON s.product_id = p.id AND s.outlet_id = 1
		ORDER BY p.id
		LIMIT 5`)
	if err != nil {
		log.Fatalf("sample products: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, qty   int
			sku, name string
			price     int64
		)
		if err := rows.Scan(&id, &sku, &name, &price, &qty); err != nil {
			log.Fatalf("scan product: %v", err)
		}
		log.Printf("#%d %s %q Rp%d stock(outlet 1)=%d", id, sku, name, price, qty)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("read products: %v", err)
	}
}
