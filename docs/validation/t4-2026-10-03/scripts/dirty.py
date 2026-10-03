import unreal
bp = unreal.load_asset("/Game/T4/BP_T4")
t = unreal.BlueprintEditorLibrary.get_basic_type_by_name("float")
print(unreal.BlueprintEditorLibrary.add_member_variable(bp, "T4Var%d" % __import__("time").time_ns(), t))
