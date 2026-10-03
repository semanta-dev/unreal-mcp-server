import unreal
les = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem)
ok = les.new_level_from_template("/Game/T4/L_WP", "/Engine/Maps/Templates/OpenWorld")
w = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem).get_editor_world()
print(ok, w.get_path_name(), w.get_world_partition() is not None if hasattr(w, "get_world_partition") else "n/a")
print([n for n in dir(unreal.WorldPartitionBlueprintLibrary) if not n.startswith("_")])
