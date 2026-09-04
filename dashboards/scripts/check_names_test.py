#!/usr/bin/env python3
import importlib.util
import pathlib
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("check_names.py")
SPEC = importlib.util.spec_from_file_location("check_names", MODULE_PATH)
check_names = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_names)


class ProbeFamilySelectorTest(unittest.TestCase):
    def test_unfiltered_turn_latency_is_reported(self):
        expr = "rate(codexlb_turn_duration_seconds_bucket{service_name=\"x\"}[5m])"
        self.assertEqual(
            check_names.probe_family_findings("dashboard.json", expr),
            [("dashboard.json", "traffic query missing probe-family selector",
              "codexlb_turn_duration_seconds_bucket")],
        )

    def test_filtered_turn_latency_is_clean(self):
        expr = ('rate(codexlb_turn_duration_seconds_bucket{service_name="x", '
                'codexlb_family!="probe"}[5m])')
        self.assertEqual(check_names.probe_family_findings("dashboard.json", expr), [])

    def test_one_filtered_term_does_not_hide_an_unfiltered_term(self):
        expr = ('codexlb_responses_total{service_name="x"} / '
                'codexlb_turns_total{service_name="x", codexlb_family!="probe"}')
        self.assertEqual(
            check_names.probe_family_findings("dashboard.json", expr),
            [("dashboard.json", "traffic query missing probe-family selector",
              "codexlb_responses_total")],
        )


if __name__ == "__main__":
    unittest.main()
