import json
import pathlib
import subprocess
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("prepare-inputs.sh")


class PrepareInputsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.git("init", "-q")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        (self.root / "code.txt").write_text("before\n")
        self.git("add", "code.txt")
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.root), *args], check=True, capture_output=True)

    def run_script(self, workflow, *args, succeeds=True):
        result = subprocess.run(["bash", str(SCRIPT), workflow, "--repo", str(self.root), *args], capture_output=True, text=True)
        if succeeds:
            self.assertEqual(result.returncode, 0, result.stderr)
            return json.loads(result.stdout)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "", "failure must not emit partial JSON")
        return result.stderr

    def test_review_combines_staged_and_unstaged_diff(self):
        (self.root / "code.txt").write_text("staged\n")
        self.git("add", "code.txt")
        (self.root / "code.txt").write_text("working\n")
        (self.root / "untracked.txt").write_text("excluded\n")
        result = self.run_script("review-change")
        self.assertIn("-before", result["diff"])
        self.assertIn("+working", result["diff"])
        self.assertNotIn("untracked.txt", result["diff"])
        self.assertIn("Untracked", result["context"])

    def test_literal_paths_requests_and_unicode(self):
        name = "$(touch marker) ' notes.txt"
        (self.root / name).write_text('Grüße "quoted"\n')
        for workflow in ["plan-change", "design-tests"]:
            result = self.run_script(workflow, "--request", "Handle $(touch marker)", "--file", name)
            self.assertIn('Grüße "quoted"', result["context"])
            self.assertEqual(result["request"], "Handle $(touch marker)")
        self.assertFalse((self.root / "marker").exists())

    def test_rejects_empty_diff_and_invalid_base(self):
        self.assertIn("nonempty", self.run_script("review-change", succeeds=False))
        self.assertIn("git command failed", self.run_script("review-change", "--base=--help", succeeds=False))

    def test_requires_nonblank_request(self):
        for request in [[], ["--request", "  "], ["--request", "é" * 4097]]:
            self.run_script("plan-change", *request, succeeds=False)

    def test_rejects_missing_binary_and_oversized_files(self):
        for name, data in [("binary", b"\x00"), ("invalid", b"\xff"), ("large", b"x" * 65537)]:
            (self.root / name).write_bytes(data)
            self.run_script("design-tests", "--request", "Test", "--file", name, succeeds=False)
        self.run_script("design-tests", "--request", "Test", "--file", "missing", succeeds=False)

    def test_rejects_escape_and_directory(self):
        (self.root / "outside").symlink_to("/etc/passwd")
        for name in ["../outside", "/etc/passwd", "outside", "."]:
            self.run_script("plan-change", "--request", "Plan", "--file", name, succeeds=False)

    def test_rejects_combined_context_overflow(self):
        (self.root / "a").write_text("x" * 33000)
        (self.root / "b").write_text("y" * 33000)
        self.assertIn("combined context", self.run_script("plan-change", "--request", "Plan", "--file", "a", "--file", "b", succeeds=False))

    def test_rejects_oversized_diff(self):
        (self.root / "code.txt").write_text("x" * 65537)
        self.assertIn("65536", self.run_script("review-change", succeeds=False))
