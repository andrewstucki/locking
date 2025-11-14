package acceptance

import (
	"os/exec"
	"testing"
)

func HasImageOrBuild(t *testing.T, name, context, dockerfile string, force bool) string {
	if !force {
		out, err := exec.Command("docker", "image", "ls", "-q", name).CombinedOutput()
		if err != nil {
			t.Fatalf("error checking docker image: %v", err)
		}
		if string(out) != "" {
			return name
		}
	}

	out, err := exec.Command("docker", "build", "-f", dockerfile, context, "-t", name).CombinedOutput()
	if err != nil {
		t.Fatalf("error building docker image: %v: %s", err, string(out))
	}
	return name
}
