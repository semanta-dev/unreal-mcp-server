# `polyworld` → `game` / `game_command`

The `polyworld` toolset (`polyworld`, `polyworld_demolish`) is **deprecated in v2.1 and removed in v2.3**. It drove the
Company-MVP through world locations; poly-world's agent API (`UPolyWorldAgentSubsystem`, declared as the project's
`.umcp.json` `game_api`) works in grid cells and ids, so the arguments differ. Both run in PIE.

```json
"game_api": {"version": 1, "object": "@subsystem:PolyWorld.PolyWorldAgentSubsystem",
             "capabilities": "GetCapabilitiesJson", "snapshot": "PeekSnapshotJson",
             "command": "ExecuteCommandJson", "events": "GetEventsSince"}
```

| `polyworld` call | Replacement | Arguments |
|---|---|---|
| `polyworld op=status` | `game op=snapshot` | — (capital, net_worth, profit_per_cycle, cities, buildings…) |
| `polyworld op=build option=<id> location=[x,y,z]` | `game_command name=place_building` | `{option_id, x, y, orientation, city_id?, search_radius?}` — `x`/`y` are **grid cells**, not world units (the snapshot's buildings and cities give cells) |
| `polyworld op=road start=[x,y] end=[x,y]` | `game_command name=place_road` | `{cells: [{x, y}, …], city_id?}` — every cell listed (a straight run is the cells between the ends); all or nothing |
| `polyworld op=select building supplier market` | `game_command name=mutate_building_graph` / `set_sourcing` | no one-to-one equivalent: the agent API edits a building's production graph (`mutate_building_graph`: add/remove nodes, connect) and sourcing policy (`set_sourcing {part_id, mode, supplier_id}`) |
| `polyworld_demolish location=[x,y,z]` | `game_command name=demolish` | `{building_id, confirm: true}` — by id (from the snapshot), not nearest-to-a-location |

Every `game_command` needs a `request_id` (re-send the same id after `outcome: unknown`; the game returns the recorded
result). `game op=capabilities` lists all 21 commands with their tiers.
