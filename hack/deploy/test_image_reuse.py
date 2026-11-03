"""Reuse planning against real local Git commits; no Docker or registry needed."""
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import image_reuse
import images


class ReuseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.git("init", "-q")
        self.git("config", "user.email", "reuse@example.test")
        self.git("config", "user.name", "Reuse test")
        self.put("build/docker/test.Dockerfile", "FROM scratch AS builder\nCOPY src/ /app/\nFROM scratch\nCOPY --from=builder /app /app\n")
        self.put("src/main.txt", "original\n")
        self.put("charts/values.yaml", "original\n")
        self.put(".dockerignore", "charts/\n")
        self.old = self.commit()
        self.spec = {"role": "server", "dockerfile": "build/docker/test.Dockerfile",
                     "repository": "localhost:5000/chill/chill-server", "platforms": ["linux/amd64"],
                     "buildArgs": {}, "image": "localhost:5000/chill/chill-server:new"}
        self.receipt = self.entry()
        self.publication = self.root / "publication.json"
        self.publish()

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], stderr=subprocess.STDOUT).decode().strip()

    def put(self, name, text):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def commit(self):
        self.git("add", "-A")
        self.git("commit", "-qm", "test change", "--allow-empty")
        return self.git("rev-parse", "HEAD")

    def entry(self):
        digest = "sha256:" + "1" * 64
        return {"image": self.spec["repository"] + ":old@" + digest, "digest": digest,
                "dockerfile": self.spec["dockerfile"], "buildArgs": copy.deepcopy(self.spec["buildArgs"]),
                "platforms": list(self.spec["platforms"]), "platformDigests": [digest],
                "platformReceipts": [{"platform": "linux/amd64", "digest": digest,
                                      "configDigest": "sha256:" + "2" * 64,
                                      "revision": self.old, "variant": ""}]}

    def publish(self, source=None, roles=None):
        self.publication.write_text(json.dumps({"sourceSHA": source or self.old,
                                               "roles": {self.spec["role"]: self.receipt} if roles is None else roles}))

    def apply(self, revision=None, path=None):
        return image_reuse.apply([self.spec], revision or self.git("rev-parse", "HEAD"),
                                 self.repo, self.repo, path or self.publication)[0]

    def assertBuild(self):
        result = self.apply()
        self.assertEqual(result["action"], "build")
        self.assertNotIn("reuse", result)

    def test_chart_change_reuses_and_freezes_receipt(self):
        self.put("charts/values.yaml", "changed\n")
        current = self.commit()
        result = self.apply(current)
        self.assertEqual(result["action"], "reuse")
        self.assertEqual(result["reuse"]["sourceSHA"], self.old)
        self.assertEqual(result["image"], self.spec["repository"] + ":old")
        self.assertIn("src", result["inputPaths"])
        self.assertIn(".dockerignore", result["inputPaths"])
        self.assertIn(self.spec["dockerfile"], result["inputPaths"])
        self.publication.write_text("not json anymore")
        self.assertEqual(result["reuse"]["receipt"], self.receipt)
        self.assertNotIn("action", self.spec)

    def test_copy_new_deleted_and_renamed_inputs_rebuild(self):
        for change in ("edit", "new", "delete", "rename"):
            with self.subTest(change=change):
                self.old = self.git("rev-parse", "HEAD")
                self.receipt = self.entry()
                self.publish()
                if change == "edit":
                    self.put("src/main.txt", "changed\n")
                elif change == "new":
                    self.put("src/new.txt", "new\n")
                elif change == "delete":
                    (self.repo / "src/main.txt").unlink()
                else:
                    (self.repo / "src/new.txt").rename(self.repo / "outside.txt")
                self.commit()
                self.assertBuild()

    def test_git_history_not_worktree_mtimes_or_contents(self):
        self.put("src/main.txt", "uncommitted\n")
        self.assertEqual(self.apply(self.old)["action"], "reuse")

    def test_textconv_cannot_hide_changed_copy_inputs(self):
        attributes = self.root / 'global-attributes'
        attributes.write_text('src/*.txt diff=masked\n')
        self.git('config', 'core.attributesFile', str(attributes))
        self.old = self.commit()
        self.receipt = self.entry()
        self.publish()
        self.git('config', 'diff.masked.textconv', "sh -c 'printf masked' ignored")
        self.put('src/main.txt', 'different raw image input\n')
        self.commit()
        self.assertBuild()

    def test_dockerfile_and_ignore_changes_rebuild(self):
        for name in (self.spec["dockerfile"], ".dockerignore", self.spec["dockerfile"] + ".dockerignore"):
            with self.subTest(path=name):
                self.old = self.git("rev-parse", "HEAD")
                self.receipt = self.entry()
                self.publish()
                self.put(name, (self.repo / name).read_text() + "\n# changed\n" if (self.repo / name).exists() else "src/\n")
                self.commit()
                self.assertBuild()

    def test_effective_args_platforms_and_power_changes_rebuild(self):
        self.spec["buildArgs"] = {"FLAG": "new"}
        self.assertBuild()
        self.spec["buildArgs"] = {}
        self.spec["platforms"] = ["linux/arm64"]
        self.assertBuild()
        self.spec["platforms"] = ["linux/amd64"]
        self.spec["powerSourceSHA"] = images.POWER_SOURCE_SHA
        self.assertBuild()

    def test_missing_role_no_configuration_and_explicit_prebuilt(self):
        self.publish(roles={})
        self.assertBuild()
        result = image_reuse.apply([self.spec], self.old, self.repo, self.repo, None)[0]
        self.assertEqual(result["action"], "build")
        self.spec["prebuilt"] = copy.deepcopy(self.receipt)
        self.assertEqual(self.apply()["action"], "prebuilt")
        with self.assertRaises(FileNotFoundError):
            self.apply(path=self.root / "missing.json")

    def test_unavailable_old_commit_rebuilds(self):
        self.receipt["platformReceipts"][0]["revision"] = "a" * 40
        self.publish()
        self.assertBuild()

    def test_mixed_and_disagreeing_role_revisions_rejected(self):
        other = copy.deepcopy(self.receipt["platformReceipts"][0])
        other.update(platform="linux/arm64", revision="b" * 40)
        self.receipt["platformReceipts"].append(other)
        self.publish()
        with self.assertRaisesRegex(ValueError, "mixed"):
            self.apply()
        self.receipt["platformReceipts"].pop()
        self.receipt["sourceSHA"] = "b" * 40
        self.publish()
        with self.assertRaisesRegex(ValueError, "disagrees"):
            self.apply()

    def test_existing_receipt_validator_rejects_bad_digest_and_repository(self):
        for field, value in (("digest", "bad"), ("image", "foreign/image:old")):
            with self.subTest(field=field):
                self.receipt = self.entry()
                self.receipt[field] = value
                self.publish()
                with self.assertRaises(ValueError):
                    self.apply()

    def test_sequential_harness_reuse_keeps_original_oci_and_arg_revision(self):
        self.spec.update(role="evaluation-harness", buildArgs={"SOURCE_SHA": self.old},
                         powerSourceSHA=images.POWER_SOURCE_SHA)
        self.receipt = self.entry()
        self.receipt["powerSourceSHA"] = images.POWER_SOURCE_SHA
        for _ in range(2):
            current = self.commit()
            self.spec["buildArgs"]["SOURCE_SHA"] = current
            # Root describes a newer execution; the image still came from old.
            self.publish(source=current)
            result = self.apply(current)
            self.assertEqual(result["action"], "reuse")
            self.assertEqual(result["reuse"]["sourceSHA"], self.old)
            self.assertEqual(result["buildArgs"]["SOURCE_SHA"], self.old)
            self.receipt = result["reuse"]["receipt"]
            self.receipt["sourceSHA"] = self.old

    def test_coarse_copy_watches_even_excluded_chart_change(self):
        self.put(self.spec["dockerfile"], "FROM scratch\nCOPY . /app\n")
        self.old = self.commit()
        self.receipt = self.entry()
        self.publish()
        self.put("charts/values.yaml", "changed\n")
        self.commit()
        self.assertBuild()
        self.assertIn(".", self.apply()["inputPaths"])

    def test_unknown_dynamic_and_context_inputs_are_conservative(self):
        for instruction in ('COPY $SOURCE /app', 'COPY src/*.txt /app', 'COPY --exclude=x src/ /app',
                            'RUN --mount=type=bind,target=/src cat /src/file',
                            'ADD https://example.test/unpinned /app', 'ONBUILD COPY src/ /app',
                            'COPY --from=$STAGE /out /app', 'COPY <<EOF /app\ntext\nEOF'):
            with self.subTest(instruction=instruction):
                self.put(self.spec["dockerfile"], "FROM scratch\n" + instruction + "\n")
                self.old = self.commit()
                self.receipt = self.entry()
                self.publish()
                self.assertEqual(self.apply()["action"], "build")
                self.assertNotIn("Git inputs changed", self.apply()["reason"])

    def test_json_copy_continuations_and_external_stage_paths(self):
        self.put(self.spec["dockerfile"], 'FROM scratch\nCOPY ["src/", "/app/"]\nCOPY --from=external/image:tag /untracked /out\nCOPY src/main.txt \\\n /main\n')
        self.old = self.commit()
        self.receipt = self.entry()
        self.publish()
        result = self.apply()
        self.assertEqual(result["action"], "reuse")
        self.assertNotIn("/untracked", result["inputPaths"])
        self.assertIn("src/main.txt", result["inputPaths"])

    def test_local_add_is_an_input_and_checksum_remote_add_is_not(self):
        self.put(self.spec["dockerfile"], "FROM scratch\nADD src/ /app/\n"
                 "ADD --checksum=sha256:" + "3" * 64 + " https://example.test/archive /archive\n")
        self.old = self.commit()
        self.receipt = self.entry()
        self.publish()
        self.assertEqual(self.apply()["action"], "reuse")
        self.put("src/new.txt", "added\n")
        self.commit()
        self.assertBuild()

    def test_matching_multi_platform_receipts_validate_independent_of_order(self):
        self.spec["platforms"].append("linux/arm64")
        self.receipt["platforms"] = list(reversed(self.spec["platforms"]))
        second = copy.deepcopy(self.receipt["platformReceipts"][0])
        second.update(platform="linux/arm64", digest="sha256:" + "4" * 64)
        self.receipt["platformReceipts"].append(second)
        self.receipt["platformDigests"].append(second["digest"])
        self.publish(source="b" * 40)
        self.assertEqual(self.apply()["action"], "reuse")
        self.receipt["platformReceipts"].pop()
        self.publish()
        with self.assertRaises(ValueError):
            self.apply()

    def test_added_export_ignore_forces_build_without_copy_blob_changes(self):
        self.put(".gitattributes", "src/* export-ignore\n")
        self.commit()
        self.assertBuild()

    def test_unchanged_export_subst_forces_build_across_unrelated_commit(self):
        self.put("src/.gitattributes", "*.txt export-subst\n")
        self.put("src/main.txt", "$Format:%H$\n")
        self.old = self.commit()
        self.receipt = self.entry()
        self.publish()
        self.put("charts/values.yaml", "unrelated change\n")
        self.commit()
        self.assertBuild()

    def test_removed_attributes_still_force_build_from_old_image_revision(self):
        self.put(".gitattributes", "src/* export-ignore\n")
        self.old = self.commit()
        self.receipt = self.entry()
        self.publish()
        (self.repo / ".gitattributes").unlink()
        self.commit()
        self.assertBuild()

    def test_repository_info_attributes_force_build(self):
        path = Path(self.git("rev-parse", "--git-path", "info/attributes"))
        if not path.is_absolute():
            path = self.repo / path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("src/* export-ignore\n")
        self.put("charts/values.yaml", "unrelated change\n")
        self.commit()
        self.assertBuild()


if __name__ == "__main__":
    unittest.main()
