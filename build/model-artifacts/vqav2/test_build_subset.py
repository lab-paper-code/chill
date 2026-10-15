import importlib.util
import tempfile
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("build_subset", Path(__file__).with_name("build_subset.py"))
BUILD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUILD)

JPEG = b"\xff\xd8\xff\xe0jpeg"


class BuildSubsetTest(unittest.TestCase):
    def test_written_subsets_have_disjoint_question_ids(self):
        records = {question_id: (JPEG, f"question {question_id}?") for question_id in range(1000, 1500)}
        subsets = BUILD.split(records)
        self.assertEqual(subsets, BUILD.split(reversed(list(records))))
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            BUILD.write(output, subsets, records)
            written = {subset: {int(path.name) for path in (output / subset).iterdir()} for subset in BUILD.SIZES}
            for subset, size in BUILD.SIZES.items():
                self.assertEqual(len(written[subset]), size)
            self.assertFalse(written["profiling"] & written["evaluation"])
            record = output / "evaluation" / str(subsets["evaluation"][0])
            self.assertEqual(sorted(path.name for path in record.iterdir()), ["image", "question"])
            self.assertEqual((record / "image").read_bytes(), JPEG)
            self.assertEqual((record / "question").read_text("utf-8"), f"question {record.name}?")

    def test_write_rejects_records_outside_the_dataset_declaration(self):
        for image, question in [(b"\x89PNG", "q?"), (JPEG, ""), (JPEG, "x" * (BUILD.MAX_QUESTION_BYTES + 1))]:
            with self.subTest(image=image[:4], question=question[:4]), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(ValueError):
                    BUILD.write(Path(directory), {"profiling": [1]}, {1: (image, question)})


if __name__ == "__main__":
    unittest.main()
