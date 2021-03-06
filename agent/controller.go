package agent

import (
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"

	"aspnet.com/benchmark"
	"aspnet.com/metrics"
)

// SubjectMap defines the mapping from a string name to the given testing subject implementation.
var SubjectMap = map[string]benchmark.Subject{
	// dummy
	"dummy": &benchmark.Dummy{},
	// signalr core
	"signalr:json:echo":         &benchmark.SignalrCoreJsonEcho{},
	"signalr:json:broadcast":    &benchmark.SignalrCoreJsonBroadcast{},
	"signalr:msgpack:echo":      &benchmark.SignalrCoreMsgpackEcho{},
	"signalr:msgpack:broadcast": &benchmark.SignalrCoreMsgpackBroadcast{},
	// signalr service
	"signalr:service:json:echo":              &benchmark.SignalrServiceJsonEcho{},
	"signalr:service:msgpack:echo":           &benchmark.SignalrServiceMsgpackEcho{},
	"signalr:service:json:broadcast":         &benchmark.SignalrServiceJsonBroadcast{},
	"signalr:service:msgpack:broadcast":      &benchmark.SignalrServiceMsgpackBroadcast{},
	"signalr:service:json:groupbroadcast":    &benchmark.SignalrServiceJsonGroupBroadcast{},
	"signalr:service:msgpack:groupbroadcast": &benchmark.SignalrServiceMsgpackGroupBroadcast{},
	// tls
	"tls:connect": &benchmark.TlsConnect{},
}

// Controller stands for a single agent and exposes management interfaces.
type Controller struct {
	AgentRole string
	Subject   benchmark.Subject
}

// Invocation represents a command invocation from the master to the agent controller.
type Invocation struct {
	Command   string
	Arguments []string
}

func argError(pos int, command string, expected string, given string) error {
	return fmt.Errorf("The %dth argument for command '%s' is %s, but it cannot be parsed from '%s'", pos, command, expected, given)
}

type SetupReply struct {
	AgentRole string
}

func (c *Controller) Setup(config *benchmark.Config, reply *SetupReply) error {
	subject, ok := SubjectMap[config.Subject]
	if !ok {
		return fmt.Errorf("Cannot find subject: " + config.Subject)
	}
	c.Subject = subject
	if err := c.Subject.Setup(config, subject); err != nil {
		return err
	}

	reply.AgentRole = c.AgentRole

	return nil
}

func (c *Controller) CollectCounters(args *struct{}, result *map[string]int64) error {
	for k, v := range c.Subject.Counters() {
		(*result)[k] += v
	}
	return nil
}

type CollectMetricsArgs struct {
	CollectProcesses []string
}

func (c *Controller) CollectMetrics(args *CollectMetricsArgs, result *metrics.AgentMetrics) error {
	cpuLoad, err := metrics.GetCPULoad()
	if err != nil {
		return err
	}

	memUsage, err := metrics.GetMachineMemoryUsage()
	if err != nil {
		return err
	}

	result.MachineMemoryUsage = memUsage.Total - memUsage.Available
	result.MachineMemoryPercentage = float64(memUsage.Total-memUsage.Available) / float64(memUsage.Total)
	result.MachineCPULoad = cpuLoad

	procUsages, err := metrics.GetProcessResourceUsages(args.CollectProcesses)
	if err != nil {
		return err
	}
	result.ProcessResourceUsages = procUsages

	return nil
}

// Invoke calls the method on the agent controller with the name Do<Command>.
func (c *Controller) Invoke(invocation *Invocation, reply *struct{}) error {
	if invocation == nil {
		return fmt.Errorf("nil Invocation")
	}

	subject := reflect.ValueOf(c.Subject)
	method := subject.MethodByName("Do" + invocation.Command)
	if !method.IsValid() {
		return fmt.Errorf("Command '%s' was not found", invocation.Command)
	}

	log.Printf("%s(%s)", invocation.Command, strings.Join(invocation.Arguments, ", "))

	argsCount := method.Type().NumIn()
	if len(invocation.Arguments) != argsCount {
		return fmt.Errorf("Command '%s' needs %d arguments, %d provided", invocation.Command, argsCount, len(invocation.Arguments))
	}

	in := make([]reflect.Value, argsCount)
	for i := 0; i < argsCount; i++ {
		stringArg := invocation.Arguments[i]
		t := method.Type().In(i)
		var arg interface{}
		var err error
		switch t.Name() {
		case "string":
			arg = stringArg
		case "bool":
			arg, err = strconv.ParseBool(stringArg)
			if err != nil {
				return argError(i, invocation.Command, t.Name(), stringArg)
			}
		case "int":
			arg, err = strconv.Atoi(stringArg)
			if err != nil {
				return argError(i, invocation.Command, t.Name(), stringArg)
			}
		case "int32":
			tmp, err := strconv.ParseInt(stringArg, 10, 32)
			if err != nil {
				return argError(i, invocation.Command, t.Name(), stringArg)
			}
			arg = int32(tmp)
		case "float32":
			tmp, err := strconv.ParseFloat(stringArg, 32)
			if err != nil {
				return argError(i, invocation.Command, t.Name(), stringArg)
			}
			arg = float32(tmp)
		case "float64":
			arg, err = strconv.ParseFloat(stringArg, 64)
			if err != nil {
				return argError(i, invocation.Command, t.Name(), stringArg)
			}
		// TODO: Support more types
		default:
			return fmt.Errorf("The %dth argument type %s for command '%s' is not supported", i, t.Name(), invocation.Command)
		}
		in[i] = reflect.ValueOf(arg)
	}

	response := method.Call(in)
	if response[0].Interface() == nil {
		return nil
	}
	return response[0].Interface().(error)
}
