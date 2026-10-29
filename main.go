// Command strata indexes a container registry into a queryable lineage graph.
package main

import "github.com/rossedman/strata/cmd"

// main hands control to the cobra root command.
func main() {
	cmd.Execute()
}
