import unreal
print([n for n in dir(unreal) if "WorldPartition" in n or "LoaderAdapter" in n or "LoadedRegion" in n][:60])
lib = unreal.WorldPartitionBlueprintLibrary
d = [x for x in lib.get_actor_descs() if str(x.get_editor_property("label")) == "WP_B"][0]
print({k: str(d.get_editor_property(k)) for k in ("guid", "is_spatially_loaded", "bounds", "actor_path", "label") if True})
