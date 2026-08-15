package cli

//go:generate env CGO_ENABLED=0 go run helpgen.go

//help:root
// Usage: maniud COMMAND [ARGUMENTS]
//
// Create and deploy Compose services, or reconcile them from Git.
//
// Commands:
//   gen          create a deployable Compose file
//   apply        deploy one Compose service
//   gitops init  register a tracked desired-state repository
//   daemon       reconcile the registered repository
//
// Run 'maniud COMMAND --help' for command-specific syntax.
//help:end

//help:gen
// Usage: maniud gen [--name SERVICE] [--output PATH] SOURCE
//
// Create a Compose file from an image reference or Docker archive member.
// The command refuses runtime create/run argv and never overwrites PATH.
//help:end

//help:apply
// Usage: maniud apply COMPOSE [SERVICE]
//
// Validate and apply one selected Compose service through the journaled transaction.
//help:end

//help:gitops
// Usage: maniud gitops init [--branch BRANCH] REPOSITORY
//
// Register the tracked desired-state repository.
//help:end

//help:gitops-init
// Usage: maniud gitops init [--branch BRANCH] REPOSITORY
//
// Register a clean checkout after proving its branch can fast-forward from origin.
//help:end

//help:daemon
// Usage: maniud daemon [--once] [--interval SECONDS]
//
// Reconcile the registered repository. The default interval is 300 seconds.
//help:end
