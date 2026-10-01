package cloudlet

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	pb "api/src/proto/cloudlandpb"
)

// runWith runs command through ExecuteCommand with prefix in place of sudo -E and returns what was sent to cland.
func runWith(t *testing.T, prefix []string, command string) []*pb.CloudletMessage {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	if len(prefix) > 0 {
		if _, err := exec.LookPath(prefix[0]); err != nil {
			t.Skipf("%s is not available", prefix[0])
		}
	}
	saved := rootPrefix
	rootPrefix = prefix
	defer func() { rootPrefix = saved }()

	stream := &fakeClientStream{}
	sender := NewStreamSender()
	sender.Attach(stream, "s")
	ExecuteCommand(sender, &pb.CommandRequest{Control: "inter=7", Command: command, MsgId: 42}, 7)
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return append([]*pb.CloudletMessage{}, stream.sent...)
}

func callbacks(msgs []*pb.CloudletMessage) (lines []string, errs []*pb.ErrorReport) {
	for _, m := range msgs {
		switch p := m.Payload.(type) {
		case *pb.CloudletMessage_Callback:
			lines = append(lines, p.Callback.Command)
		case *pb.CloudletMessage_Error:
			errs = append(errs, p.Error)
		}
	}
	return
}

// The command must not be an argument of sudo: sudo logs its command line to the journal (D7)
func TestShellCommandKeepsCommandOffTheCommandLine(t *testing.T) {
	secret := "/opt/cloudland/scripts/backend/set_user_passwd.sh '12' 'root' 'Secr3t-Pa55'"
	cmd := shellCommand(secret, []string{"NODE_ID=3"})
	if strings.Contains(strings.Join(cmd.Args, " "), "Secr3t") {
		t.Fatalf("command line carries the command: %q", cmd.Args)
	}
	if got := cmd.Args[:2]; got[0] != "sudo" || got[1] != "-E" {
		t.Errorf("command line starts with %q, want sudo -E", got)
	}
	if n := len(cmd.Args); cmd.Args[n-3] != "bash" || cmd.Args[n-2] != "-c" || cmd.Args[n-1] != commandShell {
		t.Errorf("command line ends with %q, want bash -c <command shell>", cmd.Args[n-3:])
	}
	if last := cmd.Env[len(cmd.Env)-1]; last != commandEnvName+"="+secret {
		t.Errorf("last environment entry %q, want the command", last)
	}
	if cmd.Env[0] != "NODE_ID=3" {
		t.Errorf("environment lost NODE_ID: %q", cmd.Env)
	}
}

// Callback lines, NODE_ID / TRACEPARENT, a quoted heredoc and the exit code behave as with bash -c <command>;
// the scripts do not inherit the command
func TestExecuteCommandThroughEnvironment(t *testing.T) {
	command := `echo "not a callback"
echo "|:-COMMAND-:| env.sh '$NODE_ID' '${TRACEPARENT+set}'"
cat <<'EOF'
|:-COMMAND-:| heredoc.sh '$(not expanded)' 'x'
EOF
bash -c 'echo "|:-COMMAND-:| child.sh ${CLOUDLAND_COMMAND:-unset}"'
exit 3`
	lines, errs := callbacks(runWith(t, nil, command))
	want := []string{"env.sh '7' 'set'", "heredoc.sh '$(not expanded)' 'x'", "child.sh unset"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("callbacks %q, want %q", lines, want)
	}
	if len(errs) != 1 || errs[0].ExitCode != 3 || errs[0].MsgId != 42 || errs[0].NodeId != 7 {
		t.Fatalf("error reports %v, want one with exit code 3", errs)
	}
}

func TestExecuteCommandSuccessSendsNoError(t *testing.T) {
	lines, errs := callbacks(runWith(t, nil, `echo "|:-COMMAND-:| ok.sh '1'"`))
	if len(lines) != 1 || lines[0] != "ok.sh '1'" || len(errs) != 0 {
		t.Errorf("callbacks %q, errors %v; want one callback and no error", lines, errs)
	}
}

// Through the real sudo -E of a compute node (classic sudo, "cland ALL=(ALL) NOPASSWD:ALL"); run as that user
// with CLOUDLET_TEST_SUDO=1, skipped otherwise
func TestExecuteCommandWithSudo(t *testing.T) {
	if os.Getenv("CLOUDLET_TEST_SUDO") == "" {
		t.Skip("CLOUDLET_TEST_SUDO is not set: this test needs a sudo that keeps the environment")
	}
	lines, errs := callbacks(runWith(t, rootPrefix, `echo "|:-COMMAND-:| sudo.sh '$(id -u)' '$NODE_ID' 'SudoSecret'"`))
	if len(lines) != 1 || lines[0] != "sudo.sh '0' '7' 'SudoSecret'" || len(errs) != 0 {
		t.Errorf("callbacks %q, errors %v; want the command run as root with NODE_ID", lines, errs)
	}
}

// A sudo that drops the environment (sudo-rs ignores -E) must not turn every command into a silent no-op
func TestExecuteCommandFailsWhenSudoDropsTheCommand(t *testing.T) {
	lines, errs := callbacks(runWith(t, []string{"env", "-u", commandEnvName}, `echo "|:-COMMAND-:| ran.sh"`))
	if len(lines) != 0 {
		t.Errorf("callbacks %q, want none", lines)
	}
	if len(errs) != 1 || errs[0].ExitCode != commandShellExit {
		t.Fatalf("error reports %v, want exit code %d", errs, commandShellExit)
	}
}
