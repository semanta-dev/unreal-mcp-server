import unreal
at = unreal.AssetToolsHelpers.get_asset_tools()
asset = unreal.load_asset("/Game/T4/DL_T4") or at.create_asset("DL_T4", "/Game/T4", unreal.DataLayerAsset, unreal.DataLayerFactory())
unreal.EditorAssetLibrary.save_loaded_asset(asset)
dls = unreal.get_editor_subsystem(unreal.DataLayerEditorSubsystem)
p = unreal.DataLayerCreationParameters()
p.set_editor_property("data_layer_asset", asset)
inst = dls.create_data_layer_instance(p)
sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
b = [a for a in sub.get_all_level_actors() if a.get_actor_label() == "WP_B"][0]
print("add", dls.add_actor_to_data_layer(b, inst))
print("save", unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True))
print("unload", dls.set_data_layer_is_loaded_in_editor(inst, False, True))
print("loaded", [a.get_actor_label() for a in sub.get_all_level_actors() if a.get_actor_label().startswith("WP_")])
