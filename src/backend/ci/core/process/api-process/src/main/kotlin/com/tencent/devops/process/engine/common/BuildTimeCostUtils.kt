/*
 * Tencent is pleased to support the open source community by making BK-CI 蓝鲸持续集成平台 available.
 *
 * Copyright (C) 2019 THL A29 Limited, a Tencent company.  All rights reserved.
 *
 * BK-CI 蓝鲸持续集成平台 is licensed under the MIT license.
 *
 * A copy of the MIT License is included in this file.
 *
 *
 * Terms of the MIT License:
 * ---------------------------------------------------
 * Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
 * documentation files (the "Software"), to deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all copies or substantial portions of
 * the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT
 * LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN
 * NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
 * WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 */

package com.tencent.devops.process.engine.common

import com.tencent.devops.common.pipeline.pojo.time.BuildRecordTimeCost
import com.tencent.devops.common.pipeline.enums.BuildRecordTimeStamp
import com.tencent.devops.common.pipeline.pojo.time.BuildTimestampType
import com.tencent.devops.process.engine.pojo.BuildInfo
import com.tencent.devops.process.engine.pojo.PipelineBuildContainer
import com.tencent.devops.process.engine.pojo.PipelineBuildStage
import com.tencent.devops.process.engine.pojo.PipelineBuildTask
import com.tencent.devops.process.pojo.pipeline.record.BuildRecordContainer
import com.tencent.devops.process.pojo.pipeline.record.BuildRecordStage
import com.tencent.devops.process.pojo.pipeline.record.BuildRecordTask

object BuildTimeCostUtils {

    // TODO 某层结束执行时获取内部所有耗时并计算，build对象可能因为数据清理不存在，startTime和endTime也不一定有值，需兜底

    fun generateBuildTimeCost(
        buildInfo: BuildInfo,
        stagePairs: List<Pair<BuildRecordStage, PipelineBuildStage?>>
    ): BuildRecordTimeCost {
        val startTime = buildInfo.startTime
        val endTime = buildInfo.endTime

        stagePairs.forEach { (record, build) ->
            val start = build?.startTime
            val end = build?.endTime
            val timestamps = record.timestamps
        }

        return BuildRecordTimeCost()
    }

    fun generateStageTimeCost(
        buildStage: PipelineBuildStage,
        containerPairs: List<Pair<BuildRecordContainer, PipelineBuildContainer?>>
    ): BuildRecordTimeCost {
        val startTime = buildStage.startTime
        val endTime = buildStage.endTime

        containerPairs.forEach { (record, build) ->
            val start = build?.startTime
            val end = build?.endTime
            val timestamps = record.timestamps
        }
        return BuildRecordTimeCost()
    }

    fun generateContainerTimeCost(
        buildContainer: PipelineBuildContainer,
        taskPairs: List<Pair<BuildRecordTask, PipelineBuildTask?>>
    ): BuildRecordTimeCost {
        val startTime = buildContainer.startTime
        val endTime = buildContainer.endTime

        taskPairs.forEach { (record, build) ->
            val start = build?.startTime
            val end = build?.endTime
            val timestamps = record.timestamps
        }
        return BuildRecordTimeCost()
    }

    fun generateTaskTimeCost(
        buildTask: PipelineBuildTask,
        timestamps: Map<BuildTimestampType, BuildRecordTimeStamp>
    ): BuildRecordTimeCost {
        val start = buildTask.startTime
        val end = buildTask.endTime

        return BuildRecordTimeCost()
    }
}
