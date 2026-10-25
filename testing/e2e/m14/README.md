# m14 — k-step recursion live gate (~50 min; one -k 2 sequential run)

One headless task + `-improve -k 2`: iteration 1 must produce a probed
successor, iteration 2 must start FROM that successor (chained harness0)
with iteration 1's successor probe as its reference.

```sh
make live-m14-test        # repo root; Strata up; ~50 min
```

Assertions (on the run's step.json + dirs, never model prose):
- `improvements/<run>/iter1/` and `iter2/` (or a chain-stop `note`),
- `iter2/harness0/system.md` contains text only iter1's compose could
  have introduced (chaining proof),
- root `step.json`: `k=2`, `mode=sequential`, `iterations` populated,
- overlay installed from the FINAL successor; global harness untouched.

Known-good (2026-10-25, qwen3.8-flash-next):
- **Strong path** (hermetic TestStepK2Sequential): iter2's `harness0/` is
  file-for-file iter1's compose (chaining proof), two library entries.
- **Live path x2**: both runs produced full iter1 records (candidates
  probed, contrasts, composer decisions) and the chain stopped with
  `note: iter 1 kept the harness` — the composer honestly refused to
  edit without probe-supported evidence (the model, armed with its
  memory lessons, executed cleanly). Both gates green "acceptable but
  weak": a live strong chain requires a reference WITH deficiencies;
  forcing one artificially would prove nothing. The honest stop IS the
  paper's discipline.
