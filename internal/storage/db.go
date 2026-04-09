package storage

import (
	"database/sql"
	"log"
	"os"

	_ "github.com/lib/pq"
)

var DB *sql.DB

func Init() {
	var err error
	DB, err = sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("failed to connect to postgres:", err)
	}
	if err = DB.Ping(); err != nil {
		log.Fatal("postgres not reachable:", err)
	}
	log.Println("postgres connected")
}