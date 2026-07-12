import unittest

from importlib.util import module_from_spec, spec_from_file_location
from pathlib import Path


path = Path(__file__).with_name("derive-execution-contract.py")
spec = spec_from_file_location("derive_execution_contract", path)
module = module_from_spec(spec)
assert spec and spec.loader
spec.loader.exec_module(module)


class DeriveExecutionContractTest(unittest.TestCase):
    def node(self, cpu: str) -> dict:
        return {"metadata": {"name": "edge"}, "status": {"allocatable": {"cpu": cpu}}}

    def test_integer_allocatable(self):
        contract = module.derive(self.node("4"), "100m")
        self.assertEqual(contract["cpu"]["budgetCores"], 4)
        self.assertEqual(contract["runtimeOptions"]["intraOpThreads"], 4)
        self.assertEqual(contract["runtimeOptions"]["interOpThreads"], 1)

    def test_millicpu_allocatable_is_conservatively_floored(self):
        contract = module.derive(self.node("3500m"), "100m")
        self.assertEqual(contract["cpu"]["budgetCores"], 3)

    def test_less_than_one_cpu_is_rejected(self):
        with self.assertRaises(ValueError):
            module.derive(self.node("750m"), "100m")


if __name__ == "__main__":
    unittest.main()
