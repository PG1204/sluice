// Package common holds shared types, errors, and utilities used across
// every Sluice module (engine, limiter, api, cli). Nothing here may import
// the higher-level modules — dependencies point inward, toward common.
package common

// Version is the current Sluice build version. It is wired into the CLI and
// the API's /health endpoint so a running instance can report what it is.
const Version = "0.0.0-dev"
