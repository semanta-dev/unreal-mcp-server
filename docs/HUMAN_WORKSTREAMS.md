# HUMAN WORKSTREAMS — how to actually run them

The agent has built the *harnesses* (pure-Go, tested) that consume human-produced
inputs and decide what's trustworthy. This doc is the **operator playbook**: for each
human workstream, exactly what to produce, how, in what format the harness expects,
how big it needs to be, and how to check it "cleared." Tied to AGENTIC_GAMEDEV_PLAN.md
§7.3 / §7.4 / §7.4c / §8.

Rule of thumb: **you produce the labels and the sign-off; the harness decides if the
critic/persona earned the right to gate.** Never let an un-cleared instrument gate a ship.

---

## 1. Visual calibration corpus → feeds `internal/critique`

**Goal:** prove the LLM art-critic can *order the mid-quality band* on each dimension,
per genre, before it's allowed to gate quality (§7.4). The harness (`critique.Calibrate`)
holds it to **held-out Spearman ≥ 0.70 per dimension per genre**; dimensions that clear
go in `Trusted` (may gate), the rest stay `Diagnostic` (human-in-loop).

**What to produce:** a set of scenes, each **human-ranked** on each dimension, plus the
**critic's score** for the same scene/dimension. One row = one `critique.Sample`:

```json
{ "ID": "wd_lighting_017", "Genre": "wave-defense", "Dimension": "lighting",
  "HumanScore": 0.62, "CriticScore": 0.58, "HeldOut": true }
```
Fields (must match `internal/critique/critique.go`): `ID, Genre, Dimension, HumanScore,
CriticScore, HeldOut`.

**How (step by step):**
1. **Gather scenes** per genre band (wave-defense, tycoon, economy, arena-management):
   pack demo scenes + real shipped games + **deliberately-degraded variants** (over/under-
   exposed, cube-swapped, style-clashed, de-juiced). Aim bad → mediocre → competent → pro.
2. **Dimensions** (one ranking pass each): `lighting, composition, readability,
   art-direction-coherence, material-quality, feel-quality-presence, audio, spatial,
   distinctiveness`.
3. **Rank by PAIRWISE comparison** (not absolute 0–100): show two scenes, pick the better
   on that dimension; convert wins → a 0..1 `HumanScore`. Pairwise is faster and more
   reliable than absolute scoring — the harness also reports `PairwiseAgreement`.
4. **Get the critic's score** for each scene/dimension (run the LLM critic; record
   `CriticScore` on the same 0..1 scale).
5. **Reserve a held-out slice:** mark ~30% of each (genre,dimension) group `HeldOut: true`.
   The harness only trusts a dimension on the held-out slice — so the critic can't be tuned
   to the test.
6. **Powered size (§7.4-corpus / §8):** ~20–30 scenes **per dimension per genre band** →
   ~600–850 ranked scenes total; ~3–5 person-weeks of a trained ranker. **This gates Phase-4
   start** — the critic doesn't ship as a gate until the *visual* corpus exists at this size.
7. **Ownership:** a **named human** builds the rungs. The Art-Director agent may *maintain*
   the ladder later but **must never author the rungs it is graded on** (circular).

**Check it cleared:** load your samples and run `critique.Calibrate(samples, 0.70)`. Inspect
`Report.Trusted` (dimensions that may now gate) vs `Report.Diagnostic` (stay human-in-loop).
A random or inverted critic will land in `Diagnostic` — that's the gate working.

---

## 2. Persona ladders (TWO separate bets) → feeds `internal/persona`

**Goal:** the player-persona reports retention/delight/feel from play, but may only gate the
*in-loop gradient* after its predictions clear a held-out gate vs **known human outcomes**
(§7.4c). Two bets, calibrated **separately** — feel-quality is the hardest and expected to
lag (its permanent fallback is the human feel-pulse).

**What to produce:** one row per play session = one `persona.Session` (human ground truth +
the persona's prediction for the same session):

```json
{ "ID": "wd_s042", "Genre": "wave-defense", "HeldOut": true,
  "HumanBoredomMin": 7.5, "HumanSecondSession": true, "HumanDelight": false, "HumanFeelScore": 0.4,
  "PredBoredomMin": 6.9, "PredSecondSessionProb": 0.8, "PredDelightProb": 0.3, "PredFeelScore": 0.55 }
```

**Bet 1 — RETENTION/DELIGHT (expensive; may clear per-genre):**
1. A **human plays a full loop/session** and reports: the **minute they got bored**
   (`HumanBoredomMin`), whether they'd **return** (`HumanSecondSession`), and whether they hit
   **earned satisfaction/surprise/mastery** (`HumanDelight`).
2. Run the **persona** on the same session; record its predictions (`PredBoredomMin`,
   `PredSecondSessionProb`, `PredDelightProb`).
3. This is **play-and-report**, not screenshot-ranking — tens of minutes to hours per rung,
   and you need **≥3 subjects per rung** for a reliable label.
4. **Cost (§8):** ~15–20 rungs/genre × ~3 subjects × ~1 hr × 4 genres ≈ **180–240 session-hours
   ≈ 6–10 person-weeks** — bigger than the visual corpus.

**Bet 2 — FEEL-QUALITY (hardest; plan for it to NEVER clear):**
1. Author **graded-feel variants of the SAME verb** (mushy → floaty → weighty → crisp/pro).
2. Real players **drive each variant** and rate *how it feels to control* (`HumanFeelScore`) —
   felt, not watched.
3. Record the persona's `PredFeelScore`. Its gate is **its own** Spearman vs the felt labels.
4. Expect this to lag; the **human feel-pulse is the permanent fallback** and the expected
   steady state — the in-loop feel gradient is the upside case, not the baseline.

**Held-out:** mark ~30% `HeldOut: true`; the harness requires **≥3 held-out sessions per
genre** before it will clear a leg (no trusting thin data).

**Check it cleared:** `persona.Calibrate(sessions, 0.70, 0.70)`. `ClearedLegs` = legs that may
gate the in-loop gradient; `DiagnosticLegs` = carried by the permanent human pulse.

---

## 3. Pillar-feasibility sign-off (§7.3) — the one gate no agent can self-approve

**Goal:** approve a creative pillar **only if its core fantasy is mechanically reachable**, so
identity is never skinned-then-vetoed.

**The gate (a human runs this at the Phase-2 sign-off):**
1. Write the pillar: **one genre twist + one core fantasy** (short, opinionated).
2. Ask: does the core fantasy map to **EITHER (a)** an existing scaffold axis (the fantasy IS
   a baked mechanic), **OR (b)** an explicitly-scheduled senior-authored scaffold on the
   roadmap?
   - Example (a): *"scavenger siege — every shot is scrap you can't spend"* → maps onto the
     reference scaffold's expand-vs-bank × single-vs-AoE axes. **Approve.**
   - Example (b): *"your turrets are your only light in the dark"* → a light-as-resource
     mechanic no scaffold builds → approve **only if** an "illumination-vs-firepower" scaffold
     is scheduled; else **re-pillar**.
3. If neither: **do not approve as-is** — re-pillar until reachable.
4. **Sign-off is human-owned** (Art Director + Systems/Balance advise; a human decides). Record
   the decision + which path (a/b) it cleared on. Falsification: if an approved pillar's fantasy
   is later found to be neither reachable nor scheduled, the gate failed.

---

## 4. Authored test content — unblocks the last two editor spikes

Both `input_inject` and `retarget` are *implemented/reachable*; their clean validation is
blocked only by missing test content. Author these once and the agent can validate them.

**4a. `input_inject` (§6.2) — a possessed-WASD test map.** Neither baseline game has a cleanly
player-controlled walking pawn in default PIE. Create a minimal map:
1. New level with a `PlayerStart`.
2. A GameMode whose **DefaultPawnClass** is a character with **Enhanced Input** movement bound
   (WASD → a Move IA in the active IMC) — e.g. UE's Third-Person template character, or wire the
   UltimateFPSAnimationsKIT/Mannequin character to a movement IMC.
3. Set it as the PIE map. Then the agent validates: `pie_input {key:"W", action:"hold"}` →
   `pawn_state` shows the pawn's velocity/position responding (the `verb_response` read).
   *Success = the pawn moves under injected input and stands still without it.*

**4b. `retarget` (§3.4) — a cross-skeleton IK-rig pair.** The batch-retarget mechanism is proven;
the valuable case needs a **target IK Rig on a DIFFERENT skeleton**:
1. Pick a target skeleton (e.g. UE5 Mannequin) and author an **IK Rig** for it (retarget root +
   chains: spine, arms, legs) — the sparse, flaky part the plan flags.
2. Author an **IK Retargeter** linking `IK_UndeadDraugr` (source) → the Mannequin IK Rig (target).
3. Then the agent runs `duplicate_and_retarget([AS_walk AssetData], DraugrMesh, MannequinMesh,
   retargeter)` and validates the retargeted anim's filmstrip through `in_motion_audit`
   (no foot-slide/snap). *Success = a foreign-skeleton character animates cleanly.*

---

## Where these plug into the loop
- Corpus (1) **gates Phase-4 start**; persona (2) gates only the *in-loop fun/feel gradient*
  (shipping never depends on it); pillar (3) gates *approval* and is the only enabler of deep
  cohesion; content (4) unblocks the last two spike validations.
- Run the harness after each batch of labels; only `Trusted`/`ClearedLegs` dimensions gate.
  Everything else stays human-in-loop — by design.
