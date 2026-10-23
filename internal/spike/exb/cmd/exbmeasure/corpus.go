// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"fmt"
	"strings"
)

// testCase is one corpus test (EXB-050 to EXB-055).
type testCase struct {
	Name    string   // name in result files
	Module  string   // module directory relative to the checkout root
	Pkg     string   // package directory relative to the module
	Run     string   // -test.run value
	Env     []string // extra environment (child-process scenarios run through their entry point)
	Args    []string // extra test flags after -test.count=1
	Outcome bool     // writes an EXB_OUT line (C4, C5, C7)
	Gated   bool     // counts for compatibility (EXB-065)
	// Check is a scenario entry point's parent-test assertion on a compat run (exit code, output,
	// results lines); it decides instead of the exit code. nil: the run passes when it exits 0.
	Check func(code int, out string, results []map[string]any) error
}

func at(name, module, pkg string) testCase {
	return testCase{Name: name, Module: module, Pkg: pkg, Run: "^" + name + "$", Gated: true}
}

// c1 is EXB-050's list: the Part A acceptance tests, by the names plans 2.1 to 2.3 gave them.
// AT-GOR-30 and AT-NSM-27 spawn a child process (impossible on wasip1); they run through their
// scenario entry point, and Check applies the parent test's assertion.
func c1() []testCase {
	var c []testCase
	for _, n := range []string{
		"TestATGOR01_SleepFollowsBubbleTime", "TestATGOR02_DoRunsAtVirtualTime",
		"TestATGOR03_UserTimerBeforeKernelEvent", "TestATGOR04_LogOrderAndDeterminism",
		"TestATGOR05_CrashRestartWithNetwork", "TestATGOR06_ExitedListenerRefuses",
		"TestATGOR11_UDPReaderRaceFree", "TestATGOR17_EventNodeUDPInterop",
		"TestATGOR18_PausedWriterDeferred", "TestATGOR19_ClockJumpExcludedFromMonotonic",
		"TestATGOR20_DriftAndToGlobal", "TestATGOR29_DoKeyOrder",
		"TestATFSX03_ConformanceInGoroutineMode", "TestATFSX04_StaleAfterRestart",
		"TestATFSX05_CrashLosesUnsynced", "TestATFSX06_Latency", "TestATFSX07_PausedWriteWaits",
		"TestATFSX12_MutexAcrossFileIO", "TestATFSX13_AppendSyncDeterminism",
	} {
		c = append(c, at(n, ".", "."))
	}
	gor30 := testCase{Name: "TestATGOR30_EchoReplayRate", Module: ".", Pkg: ".", Run: "^TestGoroutineScenario$",
		Env: []string{"FAULTLINE_GOR_SCENARIO=gor-30"}, Gated: true,
		Check: func(_ int, _ string, results []map[string]any) error { // the parent reads only the results lines
			det, other := 0, 0
			for _, l := range results {
				switch {
				case l["status"] == "pass":
				case l["kind"] == "determinism":
					det++
				default:
					other++
				}
			}
			if len(results) != 50 || det > 1 || other > 0 {
				return fmt.Errorf("%d results, %d determinism failures, %d other failures", len(results), det, other)
			}
			return nil
		}}
	c = append(c, gor30)
	for _, n := range []string{
		"TestATNSM01_Handshake", "TestATNSM02_Refused", "TestATNSM03_EventNodeRefuses",
		"TestATNSM04_AddressErrors", "TestATNSM05_LostSyns", "TestATNSM06_DialTimesOut",
		"TestATNSM07_JitterReordering", "TestATNSM08_Backpressure", "TestATNSM09_LossAbsorbed",
		"TestATNSM10_PartitionHealedBeforeTimeout", "TestATNSM11_ConnectionTimeout",
		"TestATNSM12_PeerCrashResets", "TestATNSM13_IsolatedCrashLearnedLater", "TestATNSM14_HalfClose",
		"TestATNSM15_CloseWithUnreadData", "TestATNSM16_WriteAfterPeerClosed", "TestATNSM17_ReadDeadlines",
		"TestATNSM18_WriteDeadline", "TestATNSM19_Backlog", "TestATNSM20_ListenerClose",
		"TestATNSM21_ListenErrors", "TestATNSM22_Loopback", "TestATNSM23_UDPDialAndReply",
		"TestATNSM24_UDPLoss", "TestATNSM25_UDPOverflow", "TestATNSM26_ProcessExitReleases",
		"TestATNSM28_PausedNodeIO", "TestATNSM29_HTTPOverNetsim", "TestATNSM31_SoakIsDeterministic",
	} {
		c = append(c, at(n, ".", "./shims/netsim"))
	}
	// nsm-27 reports nsm27-ok= with t.Logf, which a passing test prints only under -test.v; its
	// parent test runs the child with -test.v too.
	c = append(c, testCase{Name: "TestATNSM27_RunEndUnblocks", Module: ".", Pkg: "./shims/netsim",
		Run: "^TestNetsimScenario$", Env: []string{"NETSIM_SCENARIO=nsm-27"}, Args: []string{"-test.v"}, Gated: true,
		Check: func(code int, out string, _ []map[string]any) error {
			if code != 0 || !strings.Contains(out, "nsm27-ok=true") || strings.Contains(out, "still blocked") {
				return fmt.Errorf("exit %d, or nsm27-ok=true missing", code)
			}
			return nil
		}})
	c = append(c, at("TestATNSM30_GRPCHealthAndReconnect", "shims/grpcsim", "."))
	return c
}

const spike = "internal/spike/exb"

var corpora = map[string][]testCase{
	"C1": c1(),
	"C2": {at("TestGKV", spike, "./gkv")},
	"C3": {at("TestHTTPCorpus", spike, "./httpcorpus")},
	"C4": {{Name: "TestMapOrder", Module: spike, Pkg: "./order", Run: "^TestMapOrder$", Outcome: true, Gated: true}},
	"C5": {{Name: "TestSelectOrder", Module: spike, Pkg: "./order", Run: "^TestSelectOrder$", Outcome: true, Gated: true}},
	// C7 is informational (EXB-068): measured with sensitivity, never a criterion.
	"C7": {{Name: "TestGoroutineOrder", Module: spike, Pkg: "./order", Run: "^TestGoroutineOrder$", Outcome: true}},
	"C6": {
		at("TestCompatSync", spike, "./compat"), at("TestCompatEncoding", spike, "./compat"),
		at("TestCompatSynctest", spike, "./compat"), at("TestCompatCryptotest", spike, "./compat"),
		{Name: "TestCompatGRPC", Module: spike, Pkg: "./compatgrpc", Run: "^TestCompatGRPC$"},
	},
}
