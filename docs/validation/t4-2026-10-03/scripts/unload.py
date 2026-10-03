import unreal
lib = unreal.WorldPartitionBlueprintLibrary
descs = lib.get_actor_descs()
d = [x for x in descs if str(x.get_editor_property("label")) == "WP_B"]
print(len(descs), [str(x.get_editor_property("label")) for x in descs][:8])
g = d[0].get_editor_property("guid")
print("unload", lib.unload_actors([g]))
sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
print("loaded labels", [a.get_actor_label() for a in sub.get_all_level_actors() if a.get_actor_label().startswith("WP_")])
