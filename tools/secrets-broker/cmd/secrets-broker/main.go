package main

import (
	"flag"
	"log"
	"net/http"

	broker "github.com/angelosystems/stack/tools/secrets-broker"
)

func main() {
	addr := flag.String("addr", ":8089", "listen address")
	policyPath := flag.String("policy", "/etc/secrets-broker/policy.yaml", "tenant policy YAML")
	flag.Parse()

	p, err := broker.LoadPolicy(*policyPath)
	if err != nil {
		log.Fatalf("load policy: %v", err)
	}
	store := broker.NewStore()
	store.LoadSecretsFromEnv(p)

	log.Printf("secrets-broker listening on %s (tenants=%d)", *addr, len(p.Tenants))
	if err := http.ListenAndServe(*addr, broker.Handler(p, store)); err != nil {
		log.Fatal(err)
	}
}
