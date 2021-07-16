package process

import (
	"github.com/shirou/gopsutil/process"
)

// DefunctProcesses returns all the defunct processes in the node
func DefunctProcesses() ([]*process.Process, error) {
	processes, err := process.Processes()
	if err != nil {
		return nil, err
	}

	defunctProcesses := []*process.Process{}

	for _, p := range processes {
		var status string
		status, err = p.Status()

		if err == nil && status == "Z" {
			defunctProcesses = append(defunctProcesses, p)
		}
	}
	return defunctProcesses, nil
}
