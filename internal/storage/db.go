package storage

import (
	"log"
	"os"
	"database/sql"
	_ "github.com/lib/pq"
)

func ConnectDB() *sql.DB {
	connStr := os.Getenv("DATABASE_URL")

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	if err := db.Ping(); err != nil {
		log.Fatal("DB not reachable:", err)
	}

	return db
}
