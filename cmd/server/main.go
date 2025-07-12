package main

import (
	"encyclopedia-ai/internal/handlers"
	"log"
	"net/http"
)

func main() {
	http.Handle("/", http.FileServer(http.Dir("./web/static")))
	http.HandleFunc("/api/start", handlers.StartArticle)
	http.HandleFunc("/api/continue", handlers.ContinueArticle)

	log.Println("Server starting on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Could not start server: %s\n", err)
	}
}
