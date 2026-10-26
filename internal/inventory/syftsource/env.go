package syftsource

import "os"

func lookupEnv(k string) (string, bool) { return os.LookupEnv(k) }
func setenv(k, v string) error          { return os.Setenv(k, v) }
func unsetenv(k string) error           { return os.Unsetenv(k) }
